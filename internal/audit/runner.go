package audit

import (
	"context"
	"strings"
	"sync"

	"github.com/corn-xi/oci-artifact-stat/internal/registry"
)

// Fetcher retrieves the artifacts to analyze for one repository.
type Fetcher func(ctx context.Context, repo registry.Repository) ([]registry.Artifact, error)

// fetched is one repository's raw response, kept until the artifact type is
// settled.
type fetched struct {
	repo      registry.Repository
	artifacts []registry.Artifact
	err       error
}

// RunAll audits every repository, with at most concurrency requests in
// flight. Results are written by index, not appended, so row order follows
// repository order whatever finishes first; a repository's failure becomes
// its verdict rather than aborting the run.
func (a Analyzer) RunAll(ctx context.Context, scope registry.Scope, repos []registry.Repository, concurrency int, fetch Fetcher) Run {
	if concurrency < 1 {
		concurrency = 1
	}

	responses := make([]fetched, len(repos))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup

	for i, repo := range repos {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			// Every slot is filled even after cancellation: fetch returns the
			// context error immediately, which Analyze turns into a FAIL row.
			// Skipping the goroutine instead would leave a hole in the slice.
			artifacts, err := fetch(ctx, repo)
			responses[i] = fetched{repo: repo, artifacts: artifacts, err: err}
		}()
	}
	wg.Wait()

	run := a.analyzeAll(scope, responses)

	// Under "auto", a second pass can settle on a different artifact type.
	// The responses are already in memory, so this costs no requests at all
	// -- only the analysis runs again.
	if a.autoType() {
		if s, ok := run.Suggest(); ok {
			switched := a
			// Registries spell types "CHART" and the flag takes "chart"; the
			// machine-readable output must not depend on which set it.
			switched.ArtifactType = strings.ToLower(s.Type)
			run = switched.analyzeAll(scope, responses)
			run.Auto = &s
		}
	}
	return run
}

func (a Analyzer) analyzeAll(scope registry.Scope, responses []fetched) Run {
	run := Run{
		Scope:        scope,
		ArtifactType: a.wantType(),
		Explaining:   a.Explaining,
		Results:      make([]Result, len(responses)),
	}
	if a.NoPushTimes {
		run.Limits = append(run.Limits,
			"this registry does not report publish times, so stale-publish detection did not run")
	}
	for i, r := range responses {
		run.Results[i] = a.Analyze(r.repo.Name, r.artifacts, r.err)
		run.Summary.add(run.Results[i].Status)
	}
	return run
}
