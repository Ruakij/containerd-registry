package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

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

// fakeStore counts pulls, which answer pullErr; read answers readErr, or
// data once a pull succeeded.
type fakeStore struct {
	*Store
	pulls   int
	pullErr error
	data    []byte
	pulled  bool
}

func newFakeStore(ttl time.Duration) *fakeStore {
	f := &fakeStore{Store: &Store{notFoundTTL: ttl}}
	f.pull = func(context.Context, string) error {
		f.pulls++
		f.pulled = f.pullErr == nil
		return f.pullErr
	}
	return f
}

func (f *fakeStore) get(name string, dgst digest.Digest) error {
	_, _, err := f.manifest(context.Background(), name, dgst, func() (ocispec.Descriptor, []byte, error) {
		if !f.pulled {
			return ocispec.Descriptor{}, nil, ErrNotFound
		}
		return describe(ocispec.Descriptor{Digest: digest.FromBytes(f.data)}, f.data)
	})
	return err
}

func TestNotFoundTTL(t *testing.T) {
	f := newFakeStore(time.Minute)
	f.pullErr = fmt.Errorf("%w: upstream", ErrNotFound)
	for range 2 {
		if err := f.get("docker.io/x/y:1", ""); !errors.Is(err, ErrNotFound) {
			t.Fatalf("get = %v, want ErrNotFound", err)
		}
	}
	if f.pulls != 1 {
		t.Errorf("%d pulls within the TTL, want 1", f.pulls)
	}

	f.notFound["docker.io/x/y:1"] = time.Now().Add(-time.Second)
	f.pullErr = nil
	f.data = []byte(`{"schemaVersion":2,"config":{},"layers":[]}`)
	if err := f.get("docker.io/x/y:1", ""); err != nil || f.pulls != 2 {
		t.Errorf("get after expiry = %v with %d pulls, want a second pull", err, f.pulls)
	}
	if _, ok := f.notFound["docker.io/x/y:1"]; ok {
		t.Error("expired entry kept")
	}

	f.pullErr = errors.New("dial tcp: i/o timeout")
	f.pulled = false
	for range 2 {
		_ = f.get("docker.io/x/z:1", "")
	}
	if f.pulls != 4 {
		t.Errorf("%d pulls, want other errors not cached", f.pulls)
	}

	off := newFakeStore(0)
	off.pullErr = ErrNotFound
	for range 2 {
		_ = off.get("docker.io/x/y:1", "")
	}
	if off.pulls != 2 {
		t.Errorf("%d pulls with TTL 0, want 2", off.pulls)
	}
}

func TestNotFoundSweep(t *testing.T) {
	f := newFakeStore(time.Minute)
	f.notFound = map[string]time.Time{}
	for i := range maxRemembered {
		f.notFound[fmt.Sprint(i)] = time.Now().Add(-time.Second)
	}
	f.rememberNotFound("new")
	if len(f.notFound) != 1 {
		t.Errorf("%d entries after a sweep, want 1", len(f.notFound))
	}
}

func TestAttestations(t *testing.T) {
	att := digest.FromString("attestation")
	unknown := digest.FromString("unknown platform")
	image := digest.FromString("image")
	f := newFakeStore(0)
	f.pullErr = ErrNotFound
	f.pulled = true
	f.data = []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.docker.distribution.manifest.list.v2+json","manifests":[
		{"digest":%q,"platform":{"os":"linux","architecture":"amd64"}},
		{"digest":%q,"annotations":{"vnd.docker.reference.type":"attestation-manifest"}},
		{"digest":%q,"platform":{"os":"unknown","architecture":"unknown"}}]}`, image, att, unknown))
	if err := f.get("docker.io/x/y:1", ""); err != nil {
		t.Fatal(err)
	}
	f.pulled = false
	for _, d := range []digest.Digest{att, unknown, image} {
		_ = f.get("docker.io/x/y@"+d.String(), d)
	}
	if f.pulls != 1 {
		t.Errorf("%d pulls, want only the image manifest pulled", f.pulls)
	}
}
