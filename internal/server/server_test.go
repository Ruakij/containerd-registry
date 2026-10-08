package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

var (
	errNotFound        = errors.New("not found")
	errUnknownUpstream = errors.New("unknown upstream")
	errInvalidName     = errors.New("invalid name")
)

var (
	manifestBody   = []byte(`{"schemaVersion":2}`)
	manifestDigest = digest.FromBytes(manifestBody)
	blobBody       = []byte("0123456789")
	blobDigest     = digest.FromBytes(blobBody)
)

type fakeRouter struct{ ns string }

func (f *fakeRouter) Resolve(name, ns string) (string, error) {
	f.ns = ns
	switch {
	case strings.HasPrefix(name, "bad"):
		return "", fmt.Errorf("%q: %w", name, errInvalidName)
	case ns == "evil.io":
		return "", fmt.Errorf("%q: %w", ns, errUnknownUpstream)
	case name == "boom":
		return "", errors.New("router exploded")
	}
	if ns == "" {
		ns = "docker.io"
	}
	return ns + "/" + name, nil
}

type fakeStore struct{ repo, ref string }

func (f *fakeStore) Manifest(_ context.Context, repo, ref string) (ocispec.Descriptor, []byte, error) {
	f.repo, f.ref = repo, ref
	switch ref {
	case "latest", manifestDigest.String():
		return ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest, Digest: manifestDigest, Size: int64(len(manifestBody))}, manifestBody, nil
	case digest.FromString("other").String():
		return ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest, Digest: manifestDigest}, manifestBody, nil
	case "fail":
		return ocispec.Descriptor{}, nil, errors.New("containerd down")
	}
	return ocispec.Descriptor{}, nil, fmt.Errorf("%s: %w", ref, errNotFound)
}

func (f *fakeStore) Blob(_ context.Context, dgst digest.Digest) (ocispec.Descriptor, io.ReadSeekCloser, error) {
	if dgst != blobDigest {
		return ocispec.Descriptor{}, nil, errNotFound
	}
	return ocispec.Descriptor{Digest: dgst, Size: int64(len(blobBody))}, nopCloser{bytes.NewReader(blobBody)}, nil
}

type nopCloser struct{ io.ReadSeeker }

func (nopCloser) Close() error { return nil }

func newTest() (http.Handler, *fakeStore, *fakeRouter) {
	st, rt := &fakeStore{}, &fakeRouter{}
	h := New(st, rt, slog.New(slog.DiscardHandler), Errors{NotFound: errNotFound, UnknownUpstream: errUnknownUpstream, InvalidName: errInvalidName})
	return h, st, rt
}

func do(h http.Handler, method, target string, hdr ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, nil)
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func errCode(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body struct {
		Errors []struct{ Code, Message string }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body.Errors) != 1 {
		t.Fatalf("error body %q: %v", w.Body.String(), err)
	}
	return body.Errors[0].Code
}

func TestBase(t *testing.T) {
	h, _, _ := newTest()
	for _, m := range []string{http.MethodGet, http.MethodHead} {
		for _, p := range []string{"/v2/", "/v2"} {
			w := do(h, m, p)
			if w.Code != 200 || w.Header().Get("Docker-Distribution-API-Version") != "registry/2.0" {
				t.Fatalf("%s %s: %d %v", m, p, w.Code, w.Header())
			}
			if want := map[string]string{"GET": "{}", "HEAD": ""}[m]; w.Body.String() != want {
				t.Fatalf("%s %s body %q", m, p, w.Body.String())
			}
		}
	}
}

func TestHealthz(t *testing.T) {
	h, _, _ := newTest()
	if w := do(h, http.MethodGet, "/healthz"); w.Code != 200 {
		t.Fatal(w.Code)
	}
}

func TestManifest(t *testing.T) {
	h, st, rt := newTest()
	for _, ref := range []string{"latest", manifestDigest.String()} {
		w := do(h, http.MethodGet, "/v2/library/node/manifests/"+ref+"?ns=ghcr.io")
		if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), manifestBody) {
			t.Fatalf("%s: %d %q", ref, w.Code, w.Body.String())
		}
		hd := w.Header()
		for k, v := range map[string]string{
			"Content-Type":                    ocispec.MediaTypeImageManifest,
			"Docker-Content-Digest":           manifestDigest.String(),
			"Content-Length":                  fmt.Sprint(len(manifestBody)),
			"ETag":                            `"` + manifestDigest.String() + `"`,
			"Docker-Distribution-API-Version": "registry/2.0",
		} {
			if hd.Get(k) != v {
				t.Errorf("%s: header %s = %q, want %q", ref, k, hd.Get(k), v)
			}
		}
		if rt.ns != "ghcr.io" || st.repo != "ghcr.io/library/node" || st.ref != ref {
			t.Fatalf("ns %q repo %q ref %q", rt.ns, st.repo, st.ref)
		}
	}

	w := do(h, http.MethodHead, "/v2/a/b/manifests/manifests/x/manifests/latest")
	if w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("Content-Length") != fmt.Sprint(len(manifestBody)) {
		t.Fatalf("HEAD: %d %q %v", w.Code, w.Body.String(), w.Header())
	}
	if rt.ns != "" || st.repo != "docker.io/a/b/manifests/manifests/x" {
		t.Fatalf("multi-segment name: ns %q repo %q", rt.ns, st.repo)
	}
}

func TestBlob(t *testing.T) {
	h, _, rt := newTest()
	w := do(h, http.MethodGet, "/v2/x/blobs/"+blobDigest.String()+"?ns=quay.io")
	if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), blobBody) || rt.ns != "quay.io" {
		t.Fatalf("GET: %d %q ns %q", w.Code, w.Body.String(), rt.ns)
	}
	for k, v := range map[string]string{
		"Content-Type":                    "application/octet-stream",
		"Docker-Content-Digest":           blobDigest.String(),
		"Content-Length":                  fmt.Sprint(len(blobBody)),
		"Docker-Distribution-API-Version": "registry/2.0",
	} {
		if w.Header().Get(k) != v {
			t.Errorf("header %s = %q, want %q", k, w.Header().Get(k), v)
		}
	}

	w = do(h, http.MethodHead, "/v2/x/blobs/"+blobDigest.String())
	if w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("Content-Length") != fmt.Sprint(len(blobBody)) {
		t.Fatalf("HEAD: %d %q %v", w.Code, w.Body.String(), w.Header())
	}

	w = do(h, http.MethodGet, "/v2/x/blobs/"+blobDigest.String(), "Range", "bytes=2-4")
	if w.Code != http.StatusPartialContent || w.Body.String() != "234" || w.Header().Get("Content-Range") != "bytes 2-4/10" {
		t.Fatalf("Range: %d %q %v", w.Code, w.Body.String(), w.Header())
	}
}

func TestErrors(t *testing.T) {
	h, _, _ := newTest()
	for _, c := range []struct {
		method, path string
		status       int
		code         string
	}{
		{"GET", "/v2/x/manifests/missing", 404, "MANIFEST_UNKNOWN"},
		{"GET", "/v2/x/blobs/" + digest.FromString("nope").String(), 404, "BLOB_UNKNOWN"},
		{"GET", "/v2/x/manifests/latest?ns=evil.io", 404, "NAME_UNKNOWN"},
		{"GET", "/v2/x/blobs/" + blobDigest.String() + "?ns=evil.io", 404, "NAME_UNKNOWN"},
		{"GET", "/v2/bad/manifests/latest", 400, "NAME_INVALID"},
		{"GET", "/v2/x/blobs/sha256:zz", 400, "DIGEST_INVALID"},
		{"GET", "/v2/x/manifests/sha256:zz", 400, "DIGEST_INVALID"},
		{"GET", "/v2/x/manifests/-bad", 400, "TAG_INVALID"},
		{"GET", "/v2/x/manifests/" + digest.FromString("other").String(), 500, "UNKNOWN"},
		{"GET", "/v2/x/manifests/fail", 500, "UNKNOWN"},
		{"GET", "/v2/boom/manifests/latest", 500, "UNKNOWN"},
		{"PUT", "/v2/x/manifests/latest", 405, "UNSUPPORTED"},
		{"DELETE", "/v2/x/blobs/" + blobDigest.String(), 405, "UNSUPPORTED"},
		{"POST", "/v2/x/blobs/uploads/", 405, "UNSUPPORTED"},
		{"GET", "/v2/x/blobs/uploads/abc", 404, "UNSUPPORTED"},
		{"GET", "/v2/x/tags/list", 404, "UNSUPPORTED"},
		{"GET", "/v2/_catalog", 404, "UNSUPPORTED"},
		{"GET", "/v2/x/referrers/" + blobDigest.String(), 404, "UNSUPPORTED"},
		{"GET", "/other", 404, "UNSUPPORTED"},
	} {
		w := do(h, c.method, c.path)
		if w.Code != c.status || errCode(t, w) != c.code {
			t.Errorf("%s %s: %d %s, want %d %s", c.method, c.path, w.Code, w.Body.String(), c.status, c.code)
		}
		if w.Header().Get("Docker-Distribution-API-Version") != "registry/2.0" {
			t.Errorf("%s %s: missing API version header", c.method, c.path)
		}
		if c.status == 500 && strings.Contains(w.Body.String(), "containerd") {
			t.Errorf("%s %s: internal detail leaked: %s", c.method, c.path, w.Body.String())
		}
	}
}
