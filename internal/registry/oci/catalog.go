package oci

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/corn-xi/oci-artifact-stat/internal/registry"
)

// ErrNoCatalog reports that a registry will not enumerate its repositories.
// /v2/_catalog is in the spec but unimplemented on Docker Hub and GHCR among
// others, where repositories must be named explicitly.
var ErrNoCatalog = errors.New("this registry does not support listing repositories")

func (c *Client) registry() (name.Registry, error) {
	if c.insecure {
		return name.NewRegistry(c.host, name.Insecure)
	}
	return name.NewRegistry(c.host)
}

// catalog returns every repository the registry admits to having.
func (c *Client) catalog(ctx context.Context) ([]string, error) {
	reg, err := c.registry()
	if err != nil {
		return nil, err
	}
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	repos, err := remote.Catalog(ctx, reg, c.authOpt, c.transOpt)
	if err != nil {
		if unwantedHTML(err) {
			return nil, fmt.Errorf("%w: the registry answered with something other than the OCI API -- check the URL points at a registry, not a web page", ErrNoCatalog)
		}
		return nil, fmt.Errorf("%w: %v", ErrNoCatalog, err)
	}
	return repos, nil
}

// unwantedHTML reports whether err carries a web page rather than an OCI API
// response. go-containerregistry does not sanitize this itself: a 200 with
// an unexpected body fails json decoding with "invalid character '<'", and a
// non-2xx status embeds the full response body verbatim in the error text --
// seen against hub.docker.com as several kilobytes of Cloudflare HTML, a
// base64 image included, dumped straight to the terminal.
func unwantedHTML(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "invalid character") ||
		strings.Contains(msg, "<!doctype html") ||
		strings.Contains(msg, "<html")
}

// ListScopes derives scopes from the catalog by taking each repository's
// leading path segment. A plain OCI registry has no notion of a project, so
// this is the closest honest equivalent.
func (c *Client) ListScopes(ctx context.Context) ([]registry.Scope, error) {
	repos, err := c.catalog(ctx)
	if err != nil {
		return nil, err
	}

	counts := map[string]int{}
	for _, repo := range repos {
		counts[namespaceOf(repo)]++
	}

	scopes := make([]registry.Scope, 0, len(counts))
	for scope, n := range counts {
		scopes = append(scopes, registry.Scope{Name: scope, RepoCount: n, Public: true})
	}
	sort.Slice(scopes, func(i, j int) bool { return scopes[i].Name < scopes[j].Name })
	return scopes, nil
}

// ResolveScope accepts a namespace as given; there is nothing to look up.
func (c *Client) ResolveScope(_ context.Context, ref string) (registry.Scope, error) {
	if strings.TrimSpace(ref) == "" {
		return registry.Scope{}, fmt.Errorf("empty scope")
	}
	return registry.Scope{Name: ref}, nil
}

// ListRepositories returns the repositories under a namespace.
func (c *Client) ListRepositories(ctx context.Context, scope registry.Scope) ([]registry.Repository, error) {
	repos, err := c.catalog(ctx)
	if err != nil {
		return nil, err
	}

	prefix := strings.TrimSuffix(scope.Name, "/") + "/"
	out := make([]registry.Repository, 0, len(repos))
	for _, repo := range repos {
		if !strings.HasPrefix(repo, prefix) {
			continue
		}
		out = append(out, registry.Repository{
			Name:     strings.TrimPrefix(repo, prefix),
			FullName: repo,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func namespaceOf(repo string) string {
	if i := strings.LastIndex(repo, "/"); i > 0 {
		return repo[:i]
	}
	// A bare repository name lives in the registry's implicit root.
	return ""
}
