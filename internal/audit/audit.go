// Package audit turns a repository's published artifacts into a verdict.
//
// It produces structured Results, not formatted lines: what is wrong with a
// repository and how that is rendered are separate concerns, which is what
// lets one analysis feed a table, a JSON document and a CSV export.
package audit

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/corn-xi/oci-artifact-stat/internal/registry"
	"github.com/corn-xi/oci-artifact-stat/internal/version"
)

// Status is the per-repository verdict shown in the STATUS column.
type Status int

const (
	StatusOK Status = iota
	StatusWarn
	StatusFail
	StatusEmpty
)

func (s Status) String() string {
	switch s {
	case StatusOK:
		return "OK"
	case StatusWarn:
		return "WARN"
	case StatusFail:
		return "FAIL"
	case StatusEmpty:
		return "EMPTY"
	}
	return "UNKNOWN"
}

// Finding codes, kept machine-readable so JSON consumers can branch on them
// without parsing prose. They are also the values accepted by --ignore.
const (
	CodeFetchFailed         = "fetch-failed"
	CodeStalePublish        = "stale-publish"
	CodeNoVersionTags       = "no-version-tags"
	CodeOnlyPrereleases     = "only-prereleases"
	CodeWindowTruncated     = "window-truncated"
	CodePushTimeUnavailable = "push-time-unavailable"
	CodeVersionOnOtherType  = "version-on-other-type"
)

// AllCodes lists every finding code, for validating --ignore and for docs.
var AllCodes = []string{
	CodeFetchFailed,
	CodeStalePublish,
	CodeNoVersionTags,
	CodeOnlyPrereleases,
	CodeWindowTruncated,
	CodePushTimeUnavailable,
	CodeVersionOnOtherType,
}

// Finding is one diagnostic about a repository.
type Finding struct {
	Code     string
	Severity Status
	Message  string
}

// Result is the verdict for exactly one repository. Every repository yields
// one, including those that failed to fetch -- a repository that vanishes
// from the report is worse than one marked FAIL.
type Result struct {
	Repository string
	// Version is the display form: the normalized version, the raw tag under
	// --raw-tags, or a placeholder when there is nothing to show.
	Version string
	// RawTag is the tag exactly as published, empty when none was chosen.
	RawTag   string
	Status   Status
	Findings []Finding
	Err      error

	// Explain is populated only under --explain; see explain.go.
	Explain *Explain
}

// Summary counts results by status. The four counts always sum to the number
// of rows rendered.
type Summary struct {
	OK, Warn, Fail, Empty int
}

func (s Summary) Total() int { return s.OK + s.Warn + s.Fail + s.Empty }

func (s *Summary) add(st Status) {
	switch st {
	case StatusOK:
		s.OK++
	case StatusWarn:
		s.Warn++
	case StatusFail:
		s.Fail++
	case StatusEmpty:
		s.Empty++
	}
}

// Run is one complete audit.
type Run struct {
	Scope registry.Scope
	// ArtifactType is the type this run audited. Two runs of one scope under
	// different types look identical and mean different things, so the
	// output has to name it.
	ArtifactType string
	// Explaining is whether --explain was asked for.
	Explaining bool
	// Auto is set when the artifact type was chosen automatically rather
	// than given, and records why.
	Auto *Suggestion
	// Limits are checks that could not run because of the backend, as
	// opposed to anything about the repositories.
	Limits  []string
	Results []Result
	Summary Summary
}

// Analyzer holds the options affecting the verdict.
type Analyzer struct {
	// RawTags shows the tag exactly as pushed instead of the parsed version.
	RawTags bool
	// IncludePrereleases lets release candidates win the version selection.
	IncludePrereleases bool
	// ArtifactWindow is how many artifacts were requested per repository,
	// needed to notice that the answer may be truncated.
	ArtifactWindow int
	// ArtifactType is the single type a run audits; "auto" analyzes as
	// images and switches if another type reads markedly better (see
	// RunAll). Types are never mixed -- a chart and an image sharing a path
	// are different things, and comparing their versions is meaningless.
	ArtifactType string
	// Ignore suppresses findings, globally or per repository; a suppressed
	// finding affects neither the output nor the status.
	Ignore IgnoreSet
	// Explaining is whether the user asked to see the record; it gates
	// rendering, not collection.
	Explaining bool
	// NoPushTimes says the backend cannot supply push times at all. Their
	// absence is then a property of the registry, reported once for the run,
	// rather than a finding against every repository.
	NoPushTimes bool
}

// TypeAuto asks the analysis to choose the artifact type itself.
const TypeAuto = "auto"

// autoType reports whether the type is still to be decided.
func (a Analyzer) autoType() bool {
	return a.ArtifactType == "" || a.ArtifactType == TypeAuto
}

// wantType is the artifact type this run audits. Under "auto" the first pass
// reads images, which is what nearly every registry wants.
func (a Analyzer) wantType() string {
	if a.autoType() {
		return registry.TypeImage
	}
	return a.ArtifactType
}

// candidate is one version-shaped tag together with the artifact carrying it.
type candidate struct {
	tag      string
	key      version.Key
	pushTime time.Time
	hasTime  bool
}

// statusCoder is satisfied by registry errors carrying an HTTP status.
// Declared here so this package stays free of any backend import.
type statusCoder interface{ StatusCode() int }

// Analyze produces the verdict for one repository.
func (a Analyzer) Analyze(repo string, artifacts []registry.Artifact, err error) Result {
	result := a.analyze(repo, artifacts, err)
	// Always recorded, not only under --explain: it costs one pass over tags
	// already in memory, and the scope-wide skew it reveals has to be
	// available to warn about on an ordinary run.
	result.Explain = a.explain(artifacts, result.RawTag)
	return result
}

func (a Analyzer) analyze(repo string, artifacts []registry.Artifact, err error) Result {
	if err != nil {
		return a.finish(Result{Repository: repo, Version: "-", Err: err}, []Finding{{
			Code:     CodeFetchFailed,
			Severity: StatusFail,
			Message:  fmt.Sprintf("'%s': artifact fetch failed, %s", repo, failureReason(err)),
		}})
	}

	var findings []Finding

	// Only artifacts of the audited type contribute a version: a Helm chart
	// sharing the repository path is a different thing that happens to live
	// there.
	var pooledTags []string
	for _, art := range artifacts {
		if art.MatchesType(a.wantType()) {
			pooledTags = append(pooledTags, art.Tags...)
		}
	}

	candidates := a.candidates(artifacts, a.IncludePrereleases)
	if len(candidates) == 0 && !a.IncludePrereleases {
		// Nothing released. Reporting the repository as unversioned would be
		// less useful than reporting the prerelease and saying what it is.
		if fallback := a.candidates(artifacts, true); len(fallback) > 0 {
			candidates = fallback
			findings = append(findings, Finding{
				Code:     CodeOnlyPrereleases,
				Severity: StatusWarn,
				Message: fmt.Sprintf("'%s': no released version found; the reported version is a prerelease."+
					" Pass --include-prereleases to select prereleases deliberately.", repo),
			})
		}
	}

	other := a.versionsOnOtherTypes(artifacts)

	if len(candidates) == 0 {
		// The type filter, not the tagging, is the story here: every version
		// in this repository sits on a type this run excluded. Saying that is
		// far more useful than "no version tags".
		if other.found {
			tag := version.PickTag(pooledTags, a.IncludePrereleases)
			findings = append(findings, Finding{
				Code:     CodeVersionOnOtherType,
				Severity: StatusWarn,
				Message: fmt.Sprintf("'%s': no %s artifact carries a version, but %d %s artifact(s) do"+
					" (highest %s, newest tag '%s' pushed %s) -- pass --artifact-type %s to read them instead.",
					repo, strings.ToUpper(a.wantType()), other.count, other.typeName,
					other.best, other.newestTag, other.newestPush.UTC().Format(time.RFC3339),
					strings.ToLower(other.typeName)),
			})
			if tag == "" {
				return a.finish(Result{Repository: repo, Version: "<no tags>", Status: StatusEmpty}, findings)
			}
			return a.finish(Result{Repository: repo, RawTag: tag, Version: a.display(tag)}, findings)
		}

		tag := version.PickTag(pooledTags, a.IncludePrereleases)
		if tag == "" {
			return a.finish(Result{Repository: repo, Version: "<no tags>", Status: StatusEmpty}, findings)
		}

		// One finding, not both: an exhausted window means we may not have
		// looked far enough, a roomy one means the tags really carry no
		// version. The advice differs.
		if a.ArtifactWindow > 0 && len(artifacts) >= a.ArtifactWindow {
			findings = append(findings, Finding{
				Code:     CodeWindowTruncated,
				Severity: StatusWarn,
				Message: fmt.Sprintf("'%s': none of the %d most recent artifacts carried a version tag, so a"+
					" released version may lie outside the window -- selected '%s' for now;"+
					" raise --artifact-window to look further back.", repo, a.ArtifactWindow, tag),
			})
		} else {
			findings = append(findings, Finding{
				Code:     CodeNoVersionTags,
				Severity: StatusWarn,
				Message: fmt.Sprintf("'%s': no version-shaped tag among this repository's image artifacts --"+
					" selected '%s' as a fallback; verify the tagging convention.", repo, tag),
			})
		}
		return a.finish(Result{Repository: repo, RawTag: tag, Version: a.display(tag)}, findings)
	}

	top := candidates[0]
	for _, c := range candidates[1:] {
		if version.Better(c.key, c.tag, top.key, top.tag) {
			top = c
		}
	}

	// Both conditions are required: the filter must cost recency, and the
	// types must disagree about the highest version. Recency alone fires on
	// every repository of a registry that tags images unparseably and charts
	// cleanly, which is common.
	if other.found && other.best.Compare(top.key) != 0 && a.typeFilterHidesRecency(artifacts, candidates, other) {
		findings = append(findings, Finding{
			Code:     CodeVersionOnOtherType,
			Severity: StatusWarn,
			Message: fmt.Sprintf("'%s': %s artifacts report v%s (newest tag '%s', pushed %s) while the %s"+
				" artifacts inspected here report v%s, and newer %s artifacts carry no readable version"+
				" -- the two views disagree; pass --artifact-type %s to read the other one.",
				repo, other.typeName, other.best, other.newestTag,
				other.newestPush.UTC().Format(time.RFC3339),
				strings.ToUpper(a.wantType()), top.key, strings.ToUpper(a.wantType()),
				strings.ToLower(other.typeName)),
		})
	}

	findings = append(findings, a.detectStalePublish(repo, candidates, top, len(artifacts))...)
	return a.finish(Result{Repository: repo, RawTag: top.tag, Version: a.display(top.tag)}, findings)
}

// candidates collects every version-shaped tag on every image artifact.
//
// The key point versus the original heuristic: a tag is taken from anywhere in
// the artifact's tag list, not just position zero. An artifact tagged
// ["latest", "2.0.0"] used to contribute nothing, which silently suppressed
// the very anomaly this tool exists to find.
func (a Analyzer) candidates(artifacts []registry.Artifact, includePrereleases bool) []candidate {
	var out []candidate
	for _, art := range artifacts {
		if !art.MatchesType(a.wantType()) {
			continue
		}
		for _, tag := range art.Tags {
			key, ok := version.Parse(tag)
			if !ok || (key.IsPrerelease() && !includePrereleases) {
				continue
			}
			out = append(out, candidate{
				tag: tag, key: key,
				pushTime: art.PushTime, hasTime: art.HasPushTime,
			})
		}
	}
	return out
}

// otherTypeVersions summarizes version tags found on artifact types this run
// is not auditing.
type otherTypeVersions struct {
	found      bool
	count      int
	typeName   string
	best       version.Key
	newestTag  string
	newestPush time.Time
}

// versionsOnOtherTypes looks at what the type filter discarded.
//
// A repository can carry its readable version tag on a chart while the image
// beside it is tagged with something unparseable -- seen in the wild. Without
// this, the tool silently reports a stale answer and gives no hint that the
// filter is why.
func (a Analyzer) versionsOnOtherTypes(artifacts []registry.Artifact) otherTypeVersions {
	var out otherTypeVersions
	for _, art := range artifacts {
		if art.MatchesType(a.wantType()) {
			continue
		}
		for _, tag := range art.Tags {
			key, ok := version.Parse(tag)
			if !ok || (key.IsPrerelease() && !a.IncludePrereleases) {
				continue
			}
			out.count++
			if !out.found || key.Compare(out.best) > 0 {
				out.best = key
			}
			if !out.found || art.PushTime.After(out.newestPush) {
				out.newestTag, out.newestPush = tag, art.PushTime
			}
			if art.Type != "" {
				out.typeName = art.Type
			}
			out.found = true
		}
	}
	return out
}

// typeFilterHidesRecency reports whether the audited type has gone quiet --
// newer artifacts that carry no readable version -- while another type kept
// publishing versions in the same span.
func (a Analyzer) typeFilterHidesRecency(artifacts []registry.Artifact, candidates []candidate, other otherTypeVersions) bool {
	newest, ok := newestCandidate(candidates)
	if !ok {
		return false
	}
	if !other.newestPush.After(newest.pushTime) {
		return false
	}
	for _, art := range artifacts {
		if art.MatchesType(a.wantType()) && art.HasPushTime && art.PushTime.After(newest.pushTime) {
			return true
		}
	}
	return false
}

// newestCandidate returns the most recently pushed candidate.
func newestCandidate(candidates []candidate) (candidate, bool) {
	var newest candidate
	found := false
	for _, c := range candidates {
		if !c.hasTime {
			continue
		}
		if !found || c.pushTime.After(newest.pushTime) {
			newest, found = c, true
		}
	}
	return newest, found
}

// detectStalePublish checks the invariant this tool exists to enforce: the
// highest version should also be the most recently published one.
//
// Comparison is by parsed version, never by tag string. Two tags for the same
// release routinely differ by a qualifier ("2.25.0-rel" versus
// "2.25.0-rel-build42"), which is ordinary publishing, not an anomaly.
func (a Analyzer) detectStalePublish(repo string, candidates []candidate, top candidate, seen int) []Finding {
	if a.NoPushTimes {
		// The backend never has push times; that is reported once for the
		// run, not against each repository in turn.
		return nil
	}

	for _, c := range candidates {
		if !c.hasTime {
			// Push times were expected here and are missing, which is worth
			// saying: the "most recent" premise is unfounded, so the check
			// cannot run. Better than a clean bill of health never checked.
			return []Finding{{
				Code:     CodePushTimeUnavailable,
				Severity: StatusWarn,
				Message: fmt.Sprintf("'%s': the registry did not report push times, so stale-publish detection"+
					" was skipped -- the reported version is still the highest tag found.", repo),
			}}
		}
	}
	newest, found := newestCandidate(candidates)

	if !found || newest.key.Compare(top.key) >= 0 {
		// The highest version is also the newest, so versions do increase with
		// push time here and anything beyond the window is both older and
		// lower. Nothing to report, and no reason to doubt the window.
		return nil
	}

	msg := fmt.Sprintf("'%s': '%s' (v%s) was published at %s, after the higher '%s' (v%s) at %s"+
		" -- check for stale or duplicate publishing.",
		repo,
		newest.tag, newest.key, newest.pushTime.UTC().Format(time.RFC3339),
		top.tag, top.key, top.pushTime.UTC().Format(time.RFC3339))

	// Versions do not follow push order here, so a higher one could sit just
	// past the window: being older is no longer evidence of being lower. A
	// caveat on this finding, not news of its own.
	if a.ArtifactWindow > 0 && seen >= a.ArtifactWindow {
		msg += fmt.Sprintf(" Only %d of the %d artifacts inspected carried a version tag and the window was full,"+
			" so a higher version may lie beyond it; raise --artifact-window.", len(candidates), seen)
	}

	return []Finding{{Code: CodeStalePublish, Severity: StatusWarn, Message: msg}}
}

// finish applies --ignore and derives the status from what survives, so a
// suppressed finding cannot influence the exit code.
func (a Analyzer) finish(r Result, findings []Finding) Result {
	// Deliberately not set here: finish() has no access to the artifacts.
	// Analyze fills it in on the way out.
	for _, f := range findings {
		if a.Ignore.Suppressed(r.Repository, f.Code) {
			continue
		}
		r.Findings = append(r.Findings, f)
	}
	if r.Status == StatusEmpty {
		return r
	}
	r.Status = StatusOK
	for _, f := range r.Findings {
		if f.Severity == StatusFail {
			return withStatus(r, StatusFail)
		}
		if f.Severity == StatusWarn {
			r.Status = StatusWarn
		}
	}
	return r
}

func withStatus(r Result, s Status) Result {
	r.Status = s
	return r
}

func (a Analyzer) display(tag string) string {
	if a.RawTags {
		return tag
	}
	return version.Friendly(tag)
}

// failureReason describes a fetch failure.
//
// An HTTP status when one is known; otherwise whatever the backend said.
// Reporting "HTTP 000" for a registry that answered "DENIED" or "name
// unknown" throws away the only useful part of the message.
func failureReason(err error) string {
	var sc statusCoder
	if errors.As(err, &sc) {
		if code := sc.StatusCode(); code != 0 {
			return "HTTP " + strconv.Itoa(code)
		}
	}
	if err != nil {
		return firstLine(cause(err).Error())
	}
	return "no response"
}

// cause strips wrappers that repeat what the message already says: the
// backend's own labelled error, which names the repository a second time, and
// *url.Error, which repeats the whole request URL.
func cause(err error) error {
	var sc statusCoder
	if errors.As(err, &sc) && sc.StatusCode() == 0 {
		if inner := errors.Unwrap(sc.(error)); inner != nil {
			err = inner
		}
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Err != nil {
		return urlErr.Err
	}
	return err
}

// firstLine keeps a multi-line transport error from breaking the block.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}
