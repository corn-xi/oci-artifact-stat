package report

import (
	"encoding/json"
	"io"

	"github.com/corn-xi/oci-artifact-stat/internal/audit"
)

// SchemaVersion identifies the JSON document's shape. Consumers should
// branch on it; it is bumped only when a field changes meaning or goes away.
const SchemaVersion = 1

type jsonDocument struct {
	SchemaVersion int    `json:"schema_version"`
	ArtifactType  string `json:"artifact_type"`
	// ArtifactTypeAuto records that the type was chosen rather than given.
	ArtifactTypeAuto bool            `json:"artifact_type_auto,omitempty"`
	Scope            jsonScope       `json:"scope"`
	Results          []jsonResult    `json:"results"`
	Summary          jsonSummary     `json:"summary"`
	Suggestion       *jsonSuggestion `json:"suggestion,omitempty"`
}

// jsonSuggestion mirrors the human-facing hint, so CI can act on it instead
// of parsing prose.
type jsonSuggestion struct {
	ArtifactType  string `json:"artifact_type"`
	AuditedRate   int    `json:"audited_parse_rate"`
	SuggestedRate int    `json:"suggested_parse_rate"`
}

type jsonScope struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type jsonResult struct {
	Repository string        `json:"repository"`
	Version    string        `json:"version"`
	RawTag     string        `json:"raw_tag"`
	Status     string        `json:"status"`
	Findings   []jsonFinding `json:"findings"`
	Explain    *jsonExplain  `json:"explain,omitempty"`
}

type jsonExplain struct {
	Artifacts            int            `json:"artifacts"`
	ByType               map[string]int `json:"by_type"`
	Tags                 int            `json:"tags"`
	VersionTags          int            `json:"version_tags"`
	Prereleases          int            `json:"prereleases"`
	OtherTypeVersionTags int            `json:"other_type_version_tags"`
	WindowFull           bool           `json:"window_full"`
}

type jsonFinding struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

type jsonSummary struct {
	Total int `json:"total"`
	OK    int `json:"ok"`
	Warn  int `json:"warn"`
	Fail  int `json:"fail"`
	Empty int `json:"empty"`
}

// JSON writes the machine-readable report. This is the output CI is expected
// to consume, so every collection is emitted as an array rather than null.
func JSON(w io.Writer, run audit.Run) error {
	doc := jsonDocument{
		SchemaVersion: SchemaVersion,
		ArtifactType:  run.ArtifactType,
		Scope:         jsonScope{ID: run.Scope.ID, Name: run.Scope.Name},
		Results:       make([]jsonResult, 0, len(run.Results)),
		Summary: jsonSummary{
			Total: run.Summary.Total(),
			OK:    run.Summary.OK,
			Warn:  run.Summary.Warn,
			Fail:  run.Summary.Fail,
			Empty: run.Summary.Empty,
		},
	}

	if run.Auto != nil {
		doc.ArtifactTypeAuto = true
	}
	// A suggestion is only news when the type was pinned by hand; under auto
	// it has already been acted on.
	if s, ok := run.Suggest(); ok && run.Auto == nil {
		doc.Suggestion = &jsonSuggestion{
			ArtifactType:  s.Type,
			AuditedRate:   s.AuditedRate,
			SuggestedRate: s.SuggestedRate,
		}
	}

	for _, r := range run.Results {
		jr := jsonResult{
			Repository: r.Repository,
			Version:    r.Version,
			RawTag:     r.RawTag,
			Status:     r.Status.String(),
			Findings:   make([]jsonFinding, 0, len(r.Findings)),
		}
		for _, f := range r.Findings {
			jr.Findings = append(jr.Findings, jsonFinding{
				Code:     f.Code,
				Severity: f.Severity.String(),
				Message:  f.Message,
			})
		}
		// The record is always collected; it is published only when asked for.
		if run.Explaining && r.Explain != nil {
			jr.Explain = &jsonExplain{
				Artifacts:            r.Explain.Artifacts,
				ByType:               r.Explain.ByType,
				Tags:                 r.Explain.Tags,
				VersionTags:          r.Explain.VersionTags,
				Prereleases:          r.Explain.Prereleases,
				OtherTypeVersionTags: r.Explain.OtherTypeVersionTags,
				WindowFull:           r.Explain.WindowFull,
			}
		}
		doc.Results = append(doc.Results, jr)
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}
