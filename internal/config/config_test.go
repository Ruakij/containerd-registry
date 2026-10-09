package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	defaults := Config{
		Log:     Log{Level: "info"},
		HTTP:    HTTP{Address: "0.0.0.0", Port: "5000"},
		Proxy:   Proxy{NotFoundTTL: Duration(10 * time.Second)},
		Storage: Storage{Containerd: Containerd{Address: "/run/containerd/containerd.sock"}},
	}
	tests := []struct {
		name    string
		data    string
		want    *Config
		wantErr string
	}{
		{name: "empty", data: "", want: &defaults},
		{name: "empty object", data: "{}", want: &defaults},
		{
			name: "yaml",
			data: `
distSpecVersion: 1.1.1
log:
  level: debug
http:
  address: 127.0.0.1
  port: "5001"
  tls: {cert: /c, key: /k}
proxy:
  default: docker.io
  notFoundTTL: 1m30s
  registries:
    docker.io: {}
    ghcr.io: {}
storage:
  containerd:
    address: /run/k3s/containerd/containerd.sock
`,
			want: &Config{
				DistSpecVersion: "1.1.1",
				Log:             Log{Level: "debug"},
				HTTP:            HTTP{Address: "127.0.0.1", Port: "5001", TLS: &TLS{Cert: "/c", Key: "/k"}},
				Proxy:           Proxy{Default: "docker.io", NotFoundTTL: Duration(90 * time.Second), Registries: map[string]Registry{"docker.io": {}, "ghcr.io": {}}},
				Storage:         Storage{Containerd: Containerd{Address: "/run/k3s/containerd/containerd.sock"}},
			},
		},
		{
			name: "json",
			data: `{"http": {"port": "6000"}, "proxy": {"default": "quay.io", "registries": {"quay.io": {}}}}`,
			want: &Config{
				Log:     defaults.Log,
				HTTP:    HTTP{Address: "0.0.0.0", Port: "6000"},
				Proxy:   Proxy{Default: "quay.io", NotFoundTTL: defaults.Proxy.NotFoundTTL, Registries: map[string]Registry{"quay.io": {}}},
				Storage: defaults.Storage,
			},
		},
		{
			name: "notFoundTTL disabled",
			data: "proxy: {notFoundTTL: 0s}",
			want: &Config{Log: defaults.Log, HTTP: defaults.HTTP, Storage: defaults.Storage},
		},
		{name: "negative notFoundTTL", data: "proxy: {notFoundTTL: -1s}", wantErr: "proxy.notFoundTTL"},
		{name: "invalid notFoundTTL", data: "proxy: {notFoundTTL: 10}", wantErr: "not a string"},
		{name: "notFoundTTL without unit", data: `proxy: {notFoundTTL: "10"}`, wantErr: "missing unit"},
		{name: "unknown proxy key", data: "proxy: {notFoundTimeout: 1s}", wantErr: `unknown field "notFoundTimeout"`},
		{name: "unknown top level key", data: "extensions: {}", wantErr: `unknown field "extensions"`},
		{name: "unknown http key", data: "http: {auth: {}}", wantErr: `unknown field "auth"`},
		{name: "unknown accessControl", data: "http: {accessControl: {}}", wantErr: `unknown field "accessControl"`},
		{name: "unknown containerd key", data: "storage: {containerd: {hostsDir: /x}}", wantErr: `unknown field "hostsDir"`},
		{name: "unknown storage key", data: "storage: {retention: {}}", wantErr: `unknown field "retention"`},
		{name: "unknown registry key", data: "proxy: {registries: {docker.io: {rewrite: {}}}}", wantErr: `unknown field "rewrite"`},
		{name: "invalid yaml", data: "a: [", wantErr: "parse"},
		{name: "invalid log level", data: "log: {level: trace}", wantErr: "log.level"},
		{name: "tls without key", data: "http: {tls: {cert: /c}}", wantErr: "http.tls"},
		{name: "default not a registry", data: "proxy: {default: docker.io}", wantErr: "proxy.default"},
		{name: "registry key with slash", data: "proxy: {registries: {docker.io/library: {}}}", wantErr: "proxy.registries"},
		{name: "empty registry key", data: `proxy: {registries: {"": {}}}`, wantErr: "proxy.registries"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yml")
			if err := os.WriteFile(path, []byte(tt.data), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := Load(path)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Load() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Load() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yml")); err == nil {
		t.Fatal("Load() of a missing file succeeded")
	}
}
