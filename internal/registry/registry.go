// Package registry defines the vocabulary the rest of the tool speaks and the
// interfaces a backend implements.
//
// Catalog and Inspector are separate because that is where registries differ:
// listing repositories is bespoke everywhere (and /v2/_catalog is widely
// unimplemented), while reading tags is standardized.
package registry

import (
	"context"
	"strings"
	"time"
)

// TypeImage is the artifact type audited by default.
const TypeImage = "image"

// Scope is a namespace of repositories: a Harbor project, an OCI namespace,
// a GitHub organization.
type Scope struct {
	ID        int
	Name      string
	RepoCount int
	Public    bool
}

// Repository identifies one repository within a scope.
type Repository struct {
	// Name is the path with the scope prefix stripped, as the user sees it.
	Name string
	// FullName is the path as the registry reports it, prefix included.
	FullName string
}

// Artifact is one published artifact and the tags pointing at it.
type Artifact struct {
	// Type distinguishes images from other artifacts under the same path,
	// such as Helm charts.
	Type string
	Tags []string

	// PushTime is meaningful only when HasPushTime is set. Backends that
	// cannot supply it cheaply leave it unset, and the analysis degrades
	// rather than reporting unchecked repositories as healthy.
	PushTime    time.Time
	HasPushTime bool
}

// MatchesType reports whether the artifact is of the type being audited.
// Comparison is case-insensitive because registries spell types their own
// way. A missing type counts as an image, so an unexpected API shape cannot
// empty out every repository.
func (a Artifact) MatchesType(want string) bool {
	if a.Type == "" {
		return strings.EqualFold(want, TypeImage)
	}
	return strings.EqualFold(a.Type, want)
}

// Capabilities describe what a backend can answer, so the analysis can tell a
// missing answer from an unanswerable question. Without it, a limit of the
// registry is reported once per repository as though each had a problem.
type Capabilities struct {
	// PushTimes is false when the backend cannot say when an artifact was
	// published at any reasonable cost.
	PushTimes bool
	// Windowed is true when the backend returns only a bounded window of
	// recent artifacts, so exhausting it can hide older ones.
	Windowed bool
}

// Catalog reports what a registry holds. This half differs per registry.
type Catalog interface {
	ListScopes(ctx context.Context) ([]Scope, error)
	ResolveScope(ctx context.Context, ref string) (Scope, error)
	ListRepositories(ctx context.Context, scope Scope) ([]Repository, error)
}

// Inspector reports what a repository publishes. This half is standardized.
//
// Implementations return artifacts newest first; limit caps how many of the
// most recent to inspect.
type Inspector interface {
	ListArtifacts(ctx context.Context, scope Scope, repo Repository, limit int) ([]Artifact, error)
}

// Backend is one registry implementation.
type Backend interface {
	Catalog
	Inspector

	Capabilities() Capabilities
}
