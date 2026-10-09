// Package store serves manifests and blobs from the content store and image
// records of the node containerd, and pulls missing images through its CRI
// image service.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/errdefs"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sync/singleflight"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	runtime "k8s.io/cri-api/pkg/apis/runtime/v1"
)

// The kubelet pulls into this namespace, so images pulled here are used,
// shared through Spegel and garbage-collected like any other image of the node.
const namespace = "k8s.io"

// maxManifestSize follows the 4 MiB limit of the distribution spec and keeps a
// layer requested as a manifest out of memory.
const maxManifestSize = 4 << 20

// ErrNotFound means the manifest or blob exists neither in containerd nor upstream.
var ErrNotFound = errors.New("not found")

// maxRemembered bounds the not-found and attestation memories.
const maxRemembered = 10000

// Store is the containerd backend.
type Store struct {
	client *client.Client
	cri    runtime.ImageServiceClient
	pulls  singleflight.Group
	// pull is criPull, replaced in tests.
	pull        func(ctx context.Context, ref string) error
	notFoundTTL time.Duration

	mu sync.Mutex
	// notFound maps repo:tag and repo@digest refs whose pull found nothing to
	// the time until they answer not found without another pull.
	notFound map[string]time.Time
	// attestations holds the attestation manifest digests of served indexes,
	// which CRI fetches but cannot unpack.
	attestations map[digest.Digest]struct{}
}

// New connects to the containerd socket at address, which serves both the
// containerd and the CRI API. A ref whose pull found nothing answers not
// found without another pull for notFoundTTL.
func New(ctx context.Context, address string, notFoundTTL time.Duration) (*Store, error) {
	c, err := client.New(address, client.WithDefaultNamespace(namespace))
	if err != nil {
		return nil, fmt.Errorf("connect to containerd at %s: %w", address, err)
	}
	conn, ok := c.Conn().(grpc.ClientConnInterface)
	if !ok {
		c.Close()
		return nil, fmt.Errorf("containerd client connection is a %T, not a gRPC connection", c.Conn())
	}
	cri := runtime.NewImageServiceClient(conn)
	if _, err := cri.ImageFsInfo(ctx, &runtime.ImageFsInfoRequest{}); err != nil {
		c.Close()
		return nil, fmt.Errorf("CRI image service at %s: %w", address, err)
	}
	s := &Store{client: c, cri: cri, notFoundTTL: notFoundTTL}
	s.pull = s.criPull
	return s, nil
}

// Close closes the containerd connection.
func (s *Store) Close() error {
	return s.client.Close()
}

// Manifest returns the manifest or index of repo (fully qualified, such as
// docker.io/library/node) at ref, a tag or a digest, pulling the image through
// CRI when containerd does not have it.
func (s *Store) Manifest(ctx context.Context, repo, ref string) (ocispec.Descriptor, []byte, error) {
	ctx = namespaces.WithNamespace(ctx, namespace)
	name := repo + ":" + ref
	lookup := func() (ocispec.Descriptor, error) {
		img, err := s.client.ImageService().Get(ctx, name)
		if err != nil {
			return ocispec.Descriptor{}, mapErr(err)
		}
		return img.Target, nil
	}
	var dgst digest.Digest
	if strings.Contains(ref, ":") {
		var err error
		if dgst, err = digest.Parse(ref); err != nil {
			return ocispec.Descriptor{}, nil, fmt.Errorf("%w: %w", ErrNotFound, err)
		}
		name = repo + "@" + ref
		lookup = func() (ocispec.Descriptor, error) { return ocispec.Descriptor{Digest: dgst}, nil }
	}
	return s.manifest(ctx, name, dgst, func() (ocispec.Descriptor, []byte, error) { return s.read(ctx, lookup) })
}

// manifest serves name, with dgst set for a digest ref, from read, pulling
// it on a miss unless it is a known attestation manifest or upstream lacked
// it within notFoundTTL. The local read runs first, so an image pulled
// meanwhile by the kubelet is served.
func (s *Store) manifest(ctx context.Context, name string, dgst digest.Digest, read func() (ocispec.Descriptor, []byte, error)) (ocispec.Descriptor, []byte, error) {
	desc, data, err := read()
	if errors.Is(err, ErrNotFound) {
		if why := s.skipPull(name, dgst); why != "" {
			return ocispec.Descriptor{}, nil, fmt.Errorf("%w: %s %s", ErrNotFound, name, why)
		}
		if err := s.pull(ctx, name); err != nil {
			if errors.Is(err, ErrNotFound) {
				s.rememberNotFound(name)
			}
			return ocispec.Descriptor{}, nil, err
		}
		desc, data, err = read()
	}
	if err == nil {
		s.rememberAttestations(desc.MediaType, data)
	}
	return desc, data, err
}

// skipPull says why name is not worth a pull, or "" when it is.
func (s *Store) skipPull(name string, dgst digest.Digest) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.attestations[dgst]; ok && dgst != "" {
		return "is an attestation manifest"
	}
	if until, ok := s.notFound[name]; ok {
		if time.Now().Before(until) {
			return "was not found upstream recently"
		}
		delete(s.notFound, name)
	}
	return ""
}

func (s *Store) rememberNotFound(name string) {
	if s.notFoundTTL <= 0 {
		return
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.notFound == nil {
		s.notFound = map[string]time.Time{}
	}
	if len(s.notFound) >= maxRemembered {
		for n, until := range s.notFound {
			if !now.Before(until) {
				delete(s.notFound, n)
			}
		}
	}
	s.notFound[name] = now.Add(s.notFoundTTL)
}

// rememberAttestations records the attestation manifests listed in data when
// it is an index. docker clients request them by digest, and a CRI pull of
// one fails to unpack after fetching it and leaves a dangling image record.
func (s *Store) rememberAttestations(mediaType string, data []byte) {
	if mediaType != ocispec.MediaTypeImageIndex && mediaType != "application/vnd.docker.distribution.manifest.list.v2+json" {
		return
	}
	var index ocispec.Index
	if err := json.Unmarshal(data, &index); err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range index.Manifests {
		if m.Annotations["vnd.docker.reference.type"] != "attestation-manifest" &&
			(m.Platform == nil || m.Platform.OS != "unknown" || m.Platform.Architecture != "unknown") {
			continue
		}
		// ponytail: cleared at maxRemembered digests, an LRU if refetching attestations after a clear ever shows up
		if s.attestations == nil || len(s.attestations) >= maxRemembered {
			s.attestations = map[digest.Digest]struct{}{}
		}
		s.attestations[m.Digest] = struct{}{}
	}
}

func (s *Store) read(ctx context.Context, lookup func() (ocispec.Descriptor, error)) (ocispec.Descriptor, []byte, error) {
	desc, err := lookup()
	if err != nil {
		return ocispec.Descriptor{}, nil, err
	}
	cs := s.client.ContentStore()
	info, err := cs.Info(ctx, desc.Digest)
	if err != nil {
		return ocispec.Descriptor{}, nil, mapErr(err)
	}
	if info.Size > maxManifestSize {
		return ocispec.Descriptor{}, nil, fmt.Errorf("%w: %s is %d bytes, too large for a manifest", ErrNotFound, desc.Digest, info.Size)
	}
	data, err := content.ReadBlob(ctx, cs, ocispec.Descriptor{Digest: desc.Digest, Size: info.Size})
	if err != nil {
		return ocispec.Descriptor{}, nil, fmt.Errorf("read %s: %w", desc.Digest, mapErr(err))
	}
	return describe(desc, data)
}

// describe completes desc for data, after checking that data is the content of desc.Digest.
func describe(desc ocispec.Descriptor, data []byte) (ocispec.Descriptor, []byte, error) {
	if got := desc.Digest.Algorithm().FromBytes(data); got != desc.Digest {
		return ocispec.Descriptor{}, nil, fmt.Errorf("content store returned %s for %s", got, desc.Digest)
	}
	if desc.MediaType == "" {
		desc.MediaType = mediaType(data)
	}
	desc.Size = int64(len(data))
	return desc, data, nil
}

// mediaType reads the media type of a manifest or index. Docker schema 2 and
// OCI manifests carry it; for OCI manifests without one it is inferred from
// the fields, as the image spec allows it to be absent.
func mediaType(data []byte) string {
	var m struct {
		MediaType string          `json:"mediaType"`
		Manifests json.RawMessage `json:"manifests"`
	}
	if err := json.Unmarshal(data, &m); err == nil {
		switch {
		case m.MediaType != "":
			return m.MediaType
		case m.Manifests != nil:
			return ocispec.MediaTypeImageIndex
		}
	}
	return ocispec.MediaTypeImageManifest
}

// criPull pulls ref through CRI, so the node registry config applies and the
// image is unpacked and labelled like a kubelet pull. Concurrent requests for
// the same ref share one pull; a caller that gives up leaves it running for
// the others, bounded by the CRI pull progress timeout of containerd.
func (s *Store) criPull(ctx context.Context, ref string) error {
	ch := s.pulls.DoChan(ref, func() (any, error) {
		start := time.Now()
		_, err := s.cri.PullImage(context.WithoutCancel(ctx), &runtime.PullImageRequest{Image: &runtime.ImageSpec{Image: ref}})
		slog.Info("pull", "ref", ref, "duration", time.Since(start), "error", err)
		return nil, pullErr(ref, err)
	})
	select {
	case r := <-ch:
		return r.Err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// pullErr maps CRI pull errors. containerd returns them as codes.Unknown with
// the message chain, so not found is recognised in the text. A repository
// that does not exist answers 401 on Docker Hub, which containerd reports as
// pull access denied.
func pullErr(ref string, err error) error {
	if err == nil {
		return nil
	}
	msg := status.Convert(err).Message()
	// A digest that CRI cannot unpack names no image for this node, such as the
	// attestation manifests in an index, which docker clients request too.
	if status.Code(err) == codes.NotFound || strings.HasSuffix(msg, ": not found") ||
		strings.Contains(msg, "pull access denied") || strings.Contains(msg, "no match for platform") ||
		strings.Contains(msg, "mismatched image rootfs and manifest layers") {
		return fmt.Errorf("%w: pull %s: %s", ErrNotFound, ref, msg)
	}
	return fmt.Errorf("pull %s: %w", ref, err)
}

func mapErr(err error) error {
	if errdefs.IsNotFound(err) {
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	}
	return err
}

// Blob returns the blob dgst from the content store. Blobs arrive with their
// manifest, so a missing blob is not pulled.
//
// No lease holds the blob while it is read: containerd GC only removes content
// that no image references, so only an image removed during the read (kubelet
// image GC) cuts the stream, and the client retries as after any broken pull.
func (s *Store) Blob(ctx context.Context, dgst digest.Digest) (ocispec.Descriptor, io.ReadSeekCloser, error) {
	ctx = namespaces.WithNamespace(ctx, namespace)
	desc := ocispec.Descriptor{MediaType: "application/octet-stream", Digest: dgst}
	ra, err := s.client.ContentStore().ReaderAt(ctx, desc)
	if err != nil {
		return ocispec.Descriptor{}, nil, mapErr(err)
	}
	desc.Size = ra.Size()
	return desc, readSeekCloser{io.NewSectionReader(ra, 0, ra.Size()), ra}, nil
}

type readSeekCloser struct {
	*io.SectionReader
	io.Closer
}
