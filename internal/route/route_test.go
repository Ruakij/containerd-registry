package route

import (
	"errors"
	"testing"

	"github.com/Ruakij/containerd-registry/internal/config"
)

func TestResolve(t *testing.T) {
	registries := map[string]config.Registry{"docker.io": {}, "ghcr.io": {}, "localhost:5000": {}, "localhost": {}}
	withDefault := New(config.Proxy{Default: "docker.io", Registries: registries})
	noDefault := New(config.Proxy{Registries: registries})

	tests := []struct {
		name    string
		router  *Router
		repo    string
		ns      string
		want    string
		wantErr error
	}{
		{name: "ns", router: withDefault, repo: "org/app", ns: "ghcr.io", want: "ghcr.io/org/app"},
		{name: "ns keeps host-like first segment", router: withDefault, repo: "ghcr.io/app", ns: "ghcr.io", want: "ghcr.io/ghcr.io/app"},
		{name: "ns normalized", router: withDefault, repo: "node", ns: "registry-1.docker.io", want: "docker.io/library/node"},
		{name: "ns unknown", router: withDefault, repo: "org/app", ns: "quay.io", wantErr: ErrUnknownUpstream},
		{name: "host segment", router: withDefault, repo: "ghcr.io/org/app", want: "ghcr.io/org/app"},
		{name: "host segment with port", router: withDefault, repo: "localhost:5000/app", want: "localhost:5000/app"},
		{name: "host segment localhost", router: withDefault, repo: "localhost/app", want: "localhost/app"},
		{name: "host segment normalized", router: withDefault, repo: "index.docker.io/node", want: "docker.io/library/node"},
		{name: "host segment docker.io", router: withDefault, repo: "docker.io/library/node", want: "docker.io/library/node"},
		{name: "host segment unknown", router: withDefault, repo: "quay.io/org/app", wantErr: ErrUnknownUpstream},
		{name: "host-like single segment uses default", router: withDefault, repo: "my.app", want: "docker.io/library/my.app"},
		{name: "default", router: withDefault, repo: "node", want: "docker.io/library/node"},
		{name: "default with org", router: withDefault, repo: "bitnami/redis", want: "docker.io/bitnami/redis"},
		{name: "no default", router: noDefault, repo: "node", wantErr: ErrUnknownUpstream},
		{name: "no default host segment", router: noDefault, repo: "ghcr.io/org/app", want: "ghcr.io/org/app"},
		{name: "empty", router: withDefault, repo: "", wantErr: ErrInvalidName},
		{name: "empty segment", router: withDefault, repo: "org//app", wantErr: ErrInvalidName},
		{name: "trailing slash", router: withDefault, repo: "org/app/", wantErr: ErrInvalidName},
		{name: "uppercase", router: withDefault, repo: "Org/app", wantErr: ErrInvalidName},
		{name: "invalid char", router: withDefault, repo: "org/app@x", wantErr: ErrInvalidName},
		{name: "host segment without path", router: withDefault, repo: "ghcr.io/", wantErr: ErrInvalidName},
		{name: "separators", router: withDefault, repo: "a-b_c__d--e/f.g", want: "docker.io/a-b_c__d--e/f.g"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.router.Resolve(tt.repo, tt.ns)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Resolve() error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if got.String() != tt.want {
				t.Errorf("Resolve() = %q, want %q", got, tt.want)
			}
		})
	}
}
