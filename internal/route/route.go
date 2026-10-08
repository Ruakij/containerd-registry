// Package route maps repository names of requests to upstream repositories.
package route

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/Ruakij/containerd-registry/internal/config"
)

var (
	ErrUnknownUpstream = errors.New("upstream not in proxy.registries")
	ErrInvalidName     = errors.New("invalid repository name")
)

// Repository name grammar of the OCI distribution spec.
var nameRe = regexp.MustCompile(`^[a-z0-9]+((\.|_|__|-+)[a-z0-9]+)*(/[a-z0-9]+((\.|_|__|-+)[a-z0-9]+)*)*$`)

// Repo is a repository on an upstream registry.
type Repo struct {
	Host, Path string
}

// String returns the fully qualified name, as containerd names images.
func (r Repo) String() string {
	return r.Host + "/" + r.Path
}

type Router struct {
	defaultHost string
	registries  map[string]config.Registry
}

func New(p config.Proxy) *Router {
	return &Router{defaultHost: p.Default, registries: p.Registries}
}

// Resolve maps the repository name of a request and containerd's ?ns= value
// (may be empty) to an upstream repository.
func (r *Router) Resolve(name, ns string) (Repo, error) {
	var repo Repo
	switch first, rest, ok := strings.Cut(name, "/"); {
	case ns != "":
		repo = Repo{Host: ns, Path: name}
	case ok && (strings.ContainsAny(first, ".:") || first == "localhost"):
		repo = Repo{Host: first, Path: rest}
	case r.defaultHost != "":
		repo = Repo{Host: r.defaultHost, Path: name}
	default:
		return Repo{}, fmt.Errorf("%w: %q names no upstream and proxy.default is not set", ErrUnknownUpstream, name)
	}
	if !nameRe.MatchString(repo.Path) {
		return Repo{}, fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	switch repo.Host {
	case "registry-1.docker.io", "index.docker.io":
		repo.Host = "docker.io"
	}
	if repo.Host == "docker.io" && !strings.Contains(repo.Path, "/") {
		repo.Path = "library/" + repo.Path
	}
	if _, ok := r.registries[repo.Host]; !ok {
		return Repo{}, fmt.Errorf("%w: %q", ErrUnknownUpstream, repo.Host)
	}
	return repo, nil
}
