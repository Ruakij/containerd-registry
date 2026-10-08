// Package server serves the pull side of the OCI distribution API.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Store reads manifests and blobs.
type Store interface {
	Manifest(ctx context.Context, repo, ref string) (ocispec.Descriptor, []byte, error)
	Blob(ctx context.Context, dgst digest.Digest) (ocispec.Descriptor, io.ReadSeekCloser, error)
}

// Router resolves a requested name and its ?ns= upstream to a fully
// qualified repository name.
type Router interface {
	Resolve(name, ns string) (string, error)
}

// Errors are the sentinel errors of Store and Router, matched with errors.Is.
type Errors struct {
	NotFound        error
	UnknownUpstream error
	InvalidName     error
}

type server struct {
	store  Store
	router Router
	errs   Errors
	log    *slog.Logger
}

// New returns the HTTP handler of the registry.
func New(store Store, router Router, log *slog.Logger, errs Errors) http.Handler {
	return &server{store: store, router: router, errs: errs, log: log}
}

type regError struct {
	status  int
	code    string
	message string
	err     error // logged, never sent
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	rec := &recorder{ResponseWriter: w, status: http.StatusOK}
	rerr := s.serve(rec, r)
	if rerr != nil {
		writeError(rec, rerr)
	}

	level := slog.LevelDebug
	attrs := []any{"method", r.Method, "path", r.URL.RequestURI(), "status", rec.status, "bytes", rec.bytes, "duration", time.Since(start)}
	if rec.status >= 500 {
		level = slog.LevelError
		if rerr != nil && rerr.err != nil {
			attrs = append(attrs, "error", rerr.err)
		}
	}
	s.log.Log(r.Context(), level, "request", attrs...)
}

func (s *server) serve(w http.ResponseWriter, r *http.Request) *regError {
	w.Header().Set("Docker-Distribution-API-Version", "registry/2.0")
	if r.URL.Path == "/healthz" {
		return nil
	}
	if r.URL.Path != "/v2" && !strings.HasPrefix(r.URL.Path, "/v2/") {
		return &regError{http.StatusNotFound, "UNSUPPORTED", "unsupported endpoint", nil}
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return &regError{http.StatusMethodNotAllowed, "UNSUPPORTED", "only pulls are supported", nil}
	}

	path := strings.TrimPrefix(r.URL.Path, "/v2/")
	if path == "" || r.URL.Path == "/v2" {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_, _ = io.WriteString(w, "{}")
		}
		return nil
	}

	// The name may contain any number of segments, so the route is found
	// from the end: the reference or digest is always the last segment.
	for _, kind := range []string{"manifests", "blobs"} {
		i := strings.LastIndex(path, "/"+kind+"/")
		if i <= 0 {
			continue
		}
		name, ref := path[:i], path[i+len(kind)+2:]
		if ref == "" || strings.Contains(ref, "/") {
			continue
		}
		repo, err := s.router.Resolve(name, r.URL.Query().Get("ns"))
		if err != nil {
			return s.nameError(err)
		}
		if kind == "manifests" {
			return s.manifest(w, r, repo, ref)
		}
		return s.blob(w, r, ref)
	}
	return &regError{http.StatusNotFound, "UNSUPPORTED", "unsupported endpoint", nil}
}

func (s *server) nameError(err error) *regError {
	switch {
	case errors.Is(err, s.errs.UnknownUpstream):
		return &regError{http.StatusNotFound, "NAME_UNKNOWN", err.Error(), nil}
	case errors.Is(err, s.errs.InvalidName):
		return &regError{http.StatusBadRequest, "NAME_INVALID", err.Error(), nil}
	}
	return internal(err)
}

func (s *server) manifest(w http.ResponseWriter, r *http.Request, repo, ref string) *regError {
	var want digest.Digest
	if strings.Contains(ref, ":") {
		d, err := digest.Parse(ref)
		if err != nil {
			return &regError{http.StatusBadRequest, "DIGEST_INVALID", err.Error(), nil}
		}
		want = d
	} else if !tagPattern.MatchString(ref) {
		return &regError{http.StatusBadRequest, "TAG_INVALID", "invalid tag", nil}
	}

	desc, b, err := s.store.Manifest(r.Context(), repo, ref)
	if errors.Is(err, s.errs.NotFound) {
		return &regError{http.StatusNotFound, "MANIFEST_UNKNOWN", "manifest unknown", nil}
	}
	if err != nil {
		return internal(err)
	}
	if want != "" && (desc.Digest != want || want.Algorithm().FromBytes(b) != want) {
		return internal(errors.New("manifest " + repo + "@" + want.String() + " has digest " + desc.Digest.String()))
	}

	h := w.Header()
	h.Set("Content-Type", desc.MediaType)
	h.Set("Docker-Content-Digest", desc.Digest.String())
	h.Set("Content-Length", strconv.Itoa(len(b)))
	h.Set("ETag", `"`+desc.Digest.String()+`"`)
	if r.Method == http.MethodGet {
		_, _ = w.Write(b)
	}
	return nil
}

func (s *server) blob(w http.ResponseWriter, r *http.Request, ref string) *regError {
	dgst, err := digest.Parse(ref)
	if err != nil {
		return &regError{http.StatusBadRequest, "DIGEST_INVALID", err.Error(), nil}
	}
	_, rc, err := s.store.Blob(r.Context(), dgst)
	if errors.Is(err, s.errs.NotFound) {
		return &regError{http.StatusNotFound, "BLOB_UNKNOWN", "blob unknown", nil}
	}
	if err != nil {
		return internal(err)
	}
	defer rc.Close()

	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Docker-Content-Digest", dgst.String())
	h.Set("ETag", `"`+dgst.String()+`"`)
	http.ServeContent(w, r, "", time.Time{}, rc)
	return nil
}

// OCI distribution tag grammar; checked here so a bad tag never reaches a pull.
var tagPattern = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}$`)

func internal(err error) *regError {
	return &regError{http.StatusInternalServerError, "UNKNOWN", "internal error", err}
}

func writeError(w http.ResponseWriter, e *regError) {
	body, _ := json.Marshal(map[string]any{"errors": []map[string]any{{"code": e.code, "message": e.message}}})
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(e.status)
	_, _ = w.Write(body)
}

type recorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (r *recorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *recorder) Write(b []byte) (int, error) {
	n, err := r.ResponseWriter.Write(b)
	r.bytes += int64(n)
	return n, err
}
