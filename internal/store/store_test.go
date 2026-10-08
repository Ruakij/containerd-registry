package store

import (
	"errors"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestMediaType(t *testing.T) {
	for data, want := range map[string]string{
		`{"mediaType":"application/vnd.docker.distribution.manifest.v2+json","config":{}}`: "application/vnd.docker.distribution.manifest.v2+json",
		`{"schemaVersion":2,"manifests":[]}`:                                               ocispec.MediaTypeImageIndex,
		`{"schemaVersion":2,"config":{},"layers":[]}`:                                      ocispec.MediaTypeImageManifest,
		`not json`: ocispec.MediaTypeImageManifest,
	} {
		if got := mediaType([]byte(data)); got != want {
			t.Errorf("mediaType(%s) = %s, want %s", data, got, want)
		}
	}
}

func TestDescribe(t *testing.T) {
	data := []byte(`{"schemaVersion":2,"manifests":[]}`)
	desc, _, err := describe(ocispec.Descriptor{Digest: digest.FromBytes(data)}, data)
	if err != nil || desc.Size != int64(len(data)) || desc.MediaType != ocispec.MediaTypeImageIndex {
		t.Errorf("describe = %+v, %v", desc, err)
	}
	desc, _, err = describe(ocispec.Descriptor{Digest: digest.FromBytes(data), MediaType: "x"}, data)
	if err != nil || desc.MediaType != "x" {
		t.Errorf("describe with media type = %+v, %v", desc, err)
	}
	if _, _, err := describe(ocispec.Descriptor{Digest: digest.FromString("other")}, data); err == nil {
		t.Error("describe accepted content of another digest")
	}
}

func TestPullErr(t *testing.T) {
	for msg, notFound := range map[string]bool{
		`failed to pull and unpack image "docker.io/library/nope:1": failed to resolve reference "docker.io/library/nope:1": docker.io/library/nope:1: not found`:             true,
		`failed to pull and unpack image "docker.io/library/busybox@sha256:abc": no match for platform in manifest: not found`:                                                true,
		`failed to pull and unpack image "docker.io/library/alpine@sha256:abc": failed to unpack image on snapshotter overlayfs: mismatched image rootfs and manifest layers`: true,
		`failed to pull and unpack image "docker.io/x/y:1": pull access denied, repository does not exist or may require authorization`:                                       true,
		`failed to pull and unpack image "docker.io/library/busybox:1": dial tcp: i/o timeout`:                                                                                false,
	} {
		err := pullErr("ref", status.Error(codes.Unknown, msg))
		if errors.Is(err, ErrNotFound) != notFound {
			t.Errorf("pullErr(%q) = %v, want not found %v", msg, err, notFound)
		}
	}
	if !errors.Is(pullErr("ref", status.Error(codes.NotFound, "x")), ErrNotFound) {
		t.Error("codes.NotFound is not ErrNotFound")
	}
	if pullErr("ref", nil) != nil {
		t.Error("pullErr(nil) != nil")
	}
}
