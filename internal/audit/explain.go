package audit

import (
	"sort"

	"github.com/corn-xi/oci-artifact-stat/internal/registry"
	"github.com/corn-xi/oci-artifact-stat/internal/version"
)

// Explain records what the analysis saw in one repository. The rules here
// were derived from a handful of registries, so the tool shows its working
// rather than asking to be trusted on the rest.
type Explain struct {
	Artifacts int
	// ByType counts artifacts per registry-reported type; the empty type is
	// reported as "(none)".
	ByType map[string]int
	// Tags is every tag on artifacts of the audited type.
	Tags int
	// VersionTags is how many of those parsed as a version, and Prereleases
	// how many of those were prereleases.
	VersionTags int
	Prereleases int
	// OtherTypeVersionTags is version tags sitting on types this run excluded.
	OtherTypeVersionTags int
	// OtherTypes breaks the excluded types down, which is what makes it
	// possible to say *which* type reads better than the audited one.
	OtherTypes map[string]TagStats
	// WindowFull means the artifact window was exhausted, so there may be more.
	WindowFull bool
	// Chosen is the tag the analysis settled on, empty if none.
	Chosen string
}

// TagStats counts tags of one artifact type and how many parsed as versions.
type TagStats struct {
	Tags     int
	Versions int
}

// Rate is the percentage of tags that parsed as a version.
func (s TagStats) Rate() int {
	if s.Tags == 0 {
		return 0
	}
	return s.Versions * 100 / s.Tags
}

// Types returns the artifact types seen, ordered for stable output.
func (e Explain) Types() []string {
	out := make([]string, 0, len(e.ByType))
	for t := range e.ByType {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// explain builds the record for one repository.
func (a Analyzer) explain(artifacts []registry.Artifact, chosen string) *Explain {
	e := &Explain{
		Artifacts:  len(artifacts),
		ByType:     map[string]int{},
		OtherTypes: map[string]TagStats{},
		WindowFull: a.ArtifactWindow > 0 && len(artifacts) >= a.ArtifactWindow,
		Chosen:     chosen,
	}

	for _, art := range artifacts {
		name := art.Type
		if name == "" {
			name = "(none)"
		}
		e.ByType[name]++

		ours := art.MatchesType(a.wantType())
		for _, tag := range art.Tags {
			key, ok := version.Parse(tag)
			if ours {
				e.Tags++
				if ok {
					e.VersionTags++
					if key.IsPrerelease() {
						e.Prereleases++
					}
				}
				continue
			}

			stats := e.OtherTypes[name]
			stats.Tags++
			if ok {
				stats.Versions++
				e.OtherTypeVersionTags++
			}
			e.OtherTypes[name] = stats
		}
	}
	return e
}

// Totals aggregates the per-repository records of a run. The scope-wide view
// is the one that answers "is this registry shaped the way the tool assumes".
func (r Run) Totals() Explain {
	total := Explain{ByType: map[string]int{}, OtherTypes: map[string]TagStats{}}
	for _, res := range r.Results {
		if res.Explain == nil {
			continue
		}
		total.Artifacts += res.Explain.Artifacts
		total.Tags += res.Explain.Tags
		total.VersionTags += res.Explain.VersionTags
		total.Prereleases += res.Explain.Prereleases
		total.OtherTypeVersionTags += res.Explain.OtherTypeVersionTags
		if res.Explain.WindowFull {
			total.WindowFull = true
		}
		for t, n := range res.Explain.ByType {
			total.ByType[t] += n
		}
		for t, s := range res.Explain.OtherTypes {
			merged := total.OtherTypes[t]
			merged.Tags += s.Tags
			merged.Versions += s.Versions
			total.OtherTypes[t] = merged
		}
	}
	return total
}

// Thresholds for suggesting a different artifact type. Deliberately blunt:
// the situation worth reporting is a landslide, not a close call, and a
// finely-tuned rule here would be a guess dressed up as precision.
const (
	// suggestBelow is the parse rate under which the audited type is failing
	// to answer the question at all.
	suggestBelow = 50
	// suggestMargin is how many percentage points better another type must
	// read before it is worth recommending.
	suggestMargin = 30
	// suggestMinTags avoids drawing conclusions from a handful of tags.
	suggestMinTags = 10
)

// Suggestion is a recommendation to audit a different artifact type.
type Suggestion struct {
	Type          string
	AuditedRate   int
	SuggestedRate int
}

// Suggest reports a type that reads markedly better than the audited one.
// The skew is otherwise invisible: a run can answer confidently from one tag
// in five while another type carries a clean version on every one.
func (r Run) Suggest() (Suggestion, bool) {
	t := r.Totals()
	audited := TagStats{Tags: t.Tags, Versions: t.VersionTags}

	best, bestName := TagStats{}, ""
	for name, stats := range t.OtherTypes {
		if stats.Tags >= suggestMinTags && stats.Rate() > best.Rate() {
			best, bestName = stats, name
		}
	}

	if bestName == "" || audited.Rate() >= suggestBelow || best.Rate() < audited.Rate()+suggestMargin {
		return Suggestion{}, false
	}
	return Suggestion{Type: bestName, AuditedRate: audited.Rate(), SuggestedRate: best.Rate()}, true
}
