package report

import (
	"fmt"
	"io"
	"strings"

	"github.com/corn-xi/oci-artifact-stat/internal/audit"
)

// Explain writes what the analysis saw, so its view can be checked against
// the registry. Only repositories where that view might be wrong get a line;
// a hundred uniform ones would bury the interesting three, and the summary
// already covers them.
func Explain(w io.Writer, run audit.Run) error {
	for _, r := range run.Results {
		if r.Explain == nil || !notable(r) {
			continue
		}
		if _, err := fmt.Fprintf(w, "  %s: %s\n", r.Repository, describe(*r.Explain)); err != nil {
			return err
		}
	}
	return nil
}

func notable(r audit.Result) bool {
	return len(r.Findings) > 0 ||
		r.Explain.OtherTypeVersionTags > 0 ||
		r.Explain.VersionTags == 0 ||
		r.Explain.WindowFull
}

// ExplainSummary is the scope-wide line, the one that answers whether this
// registry is shaped the way the tool assumes.
func ExplainSummary(run audit.Run) string {
	t := run.Totals()
	parsed := "no tags to parse"
	if t.Tags > 0 {
		parsed = fmt.Sprintf("%d of %d %s tags parsed as versions (%d%%)",
			t.VersionTags, t.Tags, strings.ToUpper(run.ArtifactType), t.VersionTags*100/t.Tags)
	}

	line := fmt.Sprintf("Explain: %s, %s (%s); %s",
		count(len(run.Results), "repository", "repositories"),
		count(t.Artifacts, "artifact", "artifacts"), typeBreakdown(t), parsed)
	if t.Prereleases > 0 {
		line += fmt.Sprintf(", %d prerelease", t.Prereleases)
	}
	if t.OtherTypeVersionTags > 0 {
		line += fmt.Sprintf(", %d version tags on excluded types", t.OtherTypeVersionTags)
	}
	return line
}

// Hint returns the line about artifact type selection, or "" when there is
// nothing to say. Under "auto" the type has already been switched and the
// line says so, because silently changing what is measured would be worse
// than the problem it solves; with an explicit type it can only advise.
//
// It prints on an ordinary run, not only under --explain: a user cannot be
// expected to suspect that another type reads far better.
func Hint(run audit.Run) string {
	if a := run.Auto; a != nil {
		return fmt.Sprintf("Selected --artifact-type %s automatically: only %d%% of IMAGE tags here parse"+
			" as a version, against %d%% of %s tags. Pass --artifact-type image to override.",
			strings.ToLower(a.Type), a.AuditedRate, a.SuggestedRate, strings.ToUpper(a.Type))
	}

	s, ok := run.Suggest()
	if !ok {
		return ""
	}
	return fmt.Sprintf("Hint: only %d%% of %s tags here parse as a version, against %d%% of %s tags"+
		" -- try --artifact-type %s.",
		s.AuditedRate, strings.ToUpper(run.ArtifactType),
		s.SuggestedRate, strings.ToUpper(s.Type), strings.ToLower(s.Type))
}

// Limits renders what the backend could not answer, as run-level statements
// rather than a finding repeated against every repository.
func Limits(run audit.Run) []string {
	return run.Limits
}

// TimeoutHint returns advice when failures were timeouts rather than
// refusals, or "" when they were not.
func TimeoutHint(run audit.Run) string {
	n := run.TimedOut()
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("%d of %d repositories timed out; this registry is slower than the default"+
		" bounds allow -- try --http patient.", n, len(run.Results))
}

func describe(e audit.Explain) string {
	parts := []string{
		fmt.Sprintf("%s (%s)", count(e.Artifacts, "artifact", "artifacts"), typeBreakdown(e)),
		count(e.Tags, "tag", "tags"),
		count(e.VersionTags, "version", "versions"),
	}
	if e.Prereleases > 0 {
		parts = append(parts, fmt.Sprintf("%d prerelease", e.Prereleases))
	}
	if e.OtherTypeVersionTags > 0 {
		parts = append(parts, fmt.Sprintf("%d on excluded types", e.OtherTypeVersionTags))
	}
	if e.WindowFull {
		parts = append(parts, "window full")
	}

	chosen := "none"
	if e.Chosen != "" {
		chosen = e.Chosen
	}
	return strings.Join(parts, ", ") + " -> " + chosen
}

// count renders "1 artifact" / "2 artifacts". Both forms are spelled out
// rather than derived, because the rule that turns "artifact" into
// "artifacts" turns "repository" into "repositorys".
func count(n int, singular, plural string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %s", n, plural)
}

func typeBreakdown(e audit.Explain) string {
	if len(e.ByType) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(e.ByType))
	for _, t := range e.Types() {
		parts = append(parts, fmt.Sprintf("%s %d", t, e.ByType[t]))
	}
	return strings.Join(parts, ", ")
}
