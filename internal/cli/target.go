package cli

import (
	"context"
	"strings"

	"github.com/corn-xi/oci-artifact-stat/internal/auth"
	"github.com/corn-xi/oci-artifact-stat/internal/registry"
	"github.com/corn-xi/oci-artifact-stat/internal/registry/harbor"
	"github.com/corn-xi/oci-artifact-stat/internal/registry/oci"
	"github.com/corn-xi/oci-artifact-stat/internal/ui"
)

// target is what a run audits: a backend, a scope, and optionally an explicit
// list of repositories when the registry will not enumerate them.
type target struct {
	backend registry.Backend
	scope   registry.Scope
	// repos is non-nil when the repositories were named on the command line
	// rather than discovered.
	repos []registry.Repository
	// kind names the backend, for the banner.
	kind string
}

// resolveTarget decides which backend to use and what to read with it. With
// a registry URL configured the backend is detected by probing for Harbor and
// the argument is a scope; without one the arguments are full references, the
// only form that works where /v2/_catalog is unimplemented.
func resolveTarget(ctx context.Context, cfg config, creds auth.Credentials, log ui.Logger) (target, error) {
	if cfg.registryURL != "" {
		return resolveConfigured(ctx, cfg, creds, log)
	}
	return resolveReferences(cfg, creds, log)
}

func resolveConfigured(ctx context.Context, cfg config, creds auth.Credentials, log ui.Logger) (target, error) {
	harborOpts := harbor.Options{
		ConnectTimeout: cfg.connectTimeout,
		MaxTime:        cfg.maxTime,
		Retry:          harbor.RetryPolicy{Retries: cfg.retries, Delay: cfg.retryDelay},
		Logger:         log,
	}

	// A failed probe is an answer, not a transient error, so it is asked once
	// without retries.
	probeOpts := harborOpts
	probeOpts.Retry = harbor.RetryPolicy{}

	if harbor.Probe(ctx, cfg.registryURL, probeOpts) {
		backend := harbor.New(cfg.registryURL, creds, harborOpts)
		t := target{backend: backend, kind: "Harbor"}
		if len(cfg.args) > 0 {
			scope, err := backend.ResolveScope(ctx, cfg.args[0])
			if err != nil {
				return target{}, err
			}
			t.scope = scope
		}
		return t, nil
	}

	// Worth saying out loud: a user who expected Harbor otherwise sees only a
	// banner reading OCI and has to work out why.
	log.Info("%s did not answer as Harbor; reading it as a plain OCI registry.", cfg.registryURL)

	host := registryHost(cfg.registryURL)
	backend := oci.New(creds, oci.Options{
		Host:           host,
		Insecure:       strings.HasPrefix(cfg.registryURL, "http://"),
		ConnectTimeout: cfg.connectTimeout,
		MaxTime:        cfg.maxTime,
		Logger:         log,
	})
	t := target{backend: backend, kind: "OCI"}
	if len(cfg.args) > 0 {
		t.scope = registry.Scope{Name: cfg.args[0]}
	}
	return t, nil
}

// resolveReferences reads "ghcr.io/org/repo" arguments, which is how a
// registry without a usable catalog has to be addressed.
func resolveReferences(cfg config, creds auth.Credentials, log ui.Logger) (target, error) {
	var host string
	repos := make([]registry.Repository, 0, len(cfg.args))

	for _, arg := range cfg.args {
		h, path, ok := splitReference(arg)
		if !ok {
			return target{}, usagef("%q is not a repository reference: expected host/path, "+
				"or set %s to audit a scope", arg, envURL)
		}
		if host == "" {
			host = h
		} else if h != host {
			// One run, one registry: credentials and rate limits are
			// per-registry, and mixing them would make the report meaningless.
			return target{}, usagef("all references must be on one registry: got %s and %s", host, h)
		}
		repos = append(repos, registry.Repository{Name: path, FullName: path})
	}

	return target{
		backend: oci.New(creds, oci.Options{
			Host:           host,
			ConnectTimeout: cfg.connectTimeout,
			MaxTime:        cfg.maxTime,
			Logger:         log,
		}),
		scope: registry.Scope{Name: host},
		repos: repos,
		kind:  "OCI",
	}, nil
}

// allReferences reports whether every argument carries its own host, in
// which case no registry URL is needed.
func allReferences(args []string) bool {
	if len(args) == 0 {
		return false
	}
	for _, arg := range args {
		if _, _, ok := splitReference(arg); !ok {
			return false
		}
	}
	return true
}

// splitReference separates the registry host from the repository path. The
// host is what has a dot or a port, the rule OCI tooling uses to tell
// "ubuntu" from "ghcr.io/org/ubuntu". A URL is not a reference: "https://host"
// would otherwise yield a host of "https:".
func splitReference(ref string) (host, path string, ok bool) {
	ref = strings.TrimSpace(ref)
	if strings.Contains(ref, "://") {
		return "", "", false
	}
	head, rest, found := strings.Cut(ref, "/")
	if !found || rest == "" {
		return "", "", false
	}
	if !strings.ContainsAny(head, ".:") && head != "localhost" {
		return "", "", false
	}
	return head, rest, true
}
