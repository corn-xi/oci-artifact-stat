// Package oci implements the registry interfaces against any registry
// speaking the OCI Distribution Spec. Reading tags works identically
// everywhere; listing repositories does not work at all on registries that
// disable /v2/_catalog.
package oci

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/corn-xi/oci-artifact-stat/internal/auth"
	"github.com/corn-xi/oci-artifact-stat/internal/registry"
	"github.com/corn-xi/oci-artifact-stat/internal/ui"
)

// Options configures a Client.
type Options struct {
	// Host is the registry, e.g. "ghcr.io".
	Host string
	// Insecure allows plain HTTP, for local registries.
	Insecure       bool
	ConnectTimeout time.Duration
	MaxTime        time.Duration
	Logger         ui.Logger
}

// Client reads one OCI registry.
type Client struct {
	host     string
	authOpt  remote.Option
	transOpt remote.Option
	insecure bool
	// maxTime bounds a single attempt end to end. go-containerregistry
	// exposes no http.Client.Timeout equivalent (only WithTransport, which
	// cannot cap a response body read that has already started), so this is
	// applied by wrapping ctx instead -- see withTimeout.
	maxTime time.Duration
	log     ui.Logger
}

// New builds a client for one registry host.
func New(creds auth.Credentials, opts Options) *Client {
	return &Client{
		host:    opts.Host,
		authOpt: remote.WithAuth(authenticator(creds)),
		transOpt: remote.WithTransport(&http.Transport{
			DialContext:         (&net.Dialer{Timeout: opts.ConnectTimeout}).DialContext,
			TLSHandshakeTimeout: opts.ConnectTimeout,
			MaxIdleConnsPerHost: 64,
		}),
		insecure: opts.Insecure,
		maxTime:  opts.MaxTime,
		log:      opts.Logger,
	}
}

// withTimeout bounds a single attempt end to end, mirroring the Harbor
// backend's http.Client.Timeout so a hung OCI registry cannot freeze a run
// either. Zero leaves ctx as given, for callers (tests, mainly) that pass no
// deadline at all.
func (c *Client) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if c.maxTime <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, c.maxTime)
}

// authenticator adapts our credentials to the library's interface, which also
// brings the bearer-token exchange registries require. Anonymous stays
// anonymous: public registries are the common case here.
func authenticator(creds auth.Credentials) authn.Authenticator {
	if creds.Anonymous() {
		return authn.Anonymous
	}
	return authn.FromConfig(authn.AuthConfig{
		Username:      creds.Username,
		Password:      creds.Password,
		RegistryToken: creds.Token,
	})
}

// Capabilities: reading only tag names means no push times. Saying so once
// here is what stops the analysis from reporting the same limit against every
// repository it looks at.
func (c *Client) Capabilities() registry.Capabilities {
	return registry.Capabilities{PushTimes: false, Windowed: false}
}

func (c *Client) repository(scope registry.Scope, repo registry.Repository) (name.Repository, error) {
	path := repo.FullName
	if path == "" {
		path = strings.Trim(scope.Name+"/"+repo.Name, "/")
	}
	var opts []name.Option
	if c.insecure {
		opts = append(opts, name.Insecure)
	}
	return name.NewRepository(c.host+"/"+path, opts...)
}

// ListArtifacts reads a repository's tags, and only its tags: one request,
// no manifests. Type and build time would cost two more requests per tag, and
// tags/list has no order worth spending them on.
//
// Version selection needs only the tag name, so that answer stays exact;
// stale-publish detection and type filtering degrade, and say so. The limit
// is ignored because there is no window to bound -- see Capabilities.
func (c *Client) ListArtifacts(ctx context.Context, scope registry.Scope, repo registry.Repository, _ int) ([]registry.Artifact, error) {
	ref, err := c.repository(scope, repo)
	if err != nil {
		return nil, fmt.Errorf("%s/%s: %w", scope.Name, repo.Name, err)
	}

	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	tags, err := remote.List(ref, remote.WithContext(ctx), c.authOpt, c.transOpt)
	if err != nil {
		return nil, fmt.Errorf("listing tags of %s: %w", ref, err)
	}

	artifacts := make([]registry.Artifact, 0, len(tags))
	for _, tag := range tags {
		artifacts = append(artifacts, registry.Artifact{Tags: []string{tag}})
	}
	return artifacts, nil
}
