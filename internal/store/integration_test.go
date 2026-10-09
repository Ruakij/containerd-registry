//go:build containerd

package store

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func TestBusybox(t *testing.T) {
	ctx := context.Background()
	s, err := New(ctx, os.Getenv("CONTAINERD_ADDRESS"), 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// busybox:1.36 shows the cost of a pull when the node does not have it yet.
	for _, tag := range []string{"1.36", "1.36", "1.37"} {
		start := time.Now()
		desc, _, err := s.Manifest(ctx, "docker.io/library/busybox", tag)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("manifest request for %s: %v (%s %s)", tag, time.Since(start), desc.MediaType, desc.Digest)
	}

	_, data, err := s.Manifest(ctx, "docker.io/library/busybox", "1.37")
	if err != nil {
		t.Fatal(err)
	}
	var index ocispec.Index
	if err := json.Unmarshal(data, &index); err != nil {
		t.Fatal(err)
	}
	var child, other digest.Digest
	for _, m := range index.Manifests {
		switch {
		case m.Platform == nil:
		case m.Platform.OS == "linux" && m.Platform.Architecture == "amd64":
			child = m.Digest
		case m.Platform.Architecture == "s390x":
			other = m.Digest
		}
	}

	desc, data, err := s.Manifest(ctx, "docker.io/library/busybox", child.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("child %s %s", desc.MediaType, desc.Digest)
	var manifest ocispec.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}

	layer := manifest.Layers[0]
	start := time.Now()
	desc, r, err := s.Blob(ctx, layer.Digest)
	if err != nil {
		t.Fatal(err)
	}
	verifier := layer.Digest.Verifier()
	n, err := io.Copy(verifier, r)
	r.Close()
	if err != nil || n != layer.Size || desc.Size != layer.Size || !verifier.Verified() {
		t.Fatalf("blob: %d of %d bytes, verified %v, %v", n, layer.Size, verifier.Verified(), err)
	}
	t.Logf("blob %s: %d bytes in %v", layer.Digest, n, time.Since(start))

	// CRI pulls a platform manifest requested by digest whatever its platform
	// (containerd v2.3), so this documents the behaviour instead of asserting it.
	start = time.Now()
	desc, _, err = s.Manifest(ctx, "docker.io/library/busybox", other.String())
	t.Logf("manifest of another platform in %v: %s %v", time.Since(start), desc.Digest, err)
	_, _, err = s.Manifest(ctx, "docker.io/library/busybox", "does-not-exist")
	t.Logf("missing tag: %v", err)
	if !errors.Is(err, ErrNotFound) {
		t.Error("want ErrNotFound")
	}
	_, _, err = s.Manifest(ctx, "docker.io/ruakij/does-not-exist", "1")
	t.Logf("missing repository: %v", err)
	if !errors.Is(err, ErrNotFound) {
		t.Error("want ErrNotFound")
	}
	if _, _, err := s.Blob(ctx, digest.FromString("missing")); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing blob: %v, want ErrNotFound", err)
	}
}
