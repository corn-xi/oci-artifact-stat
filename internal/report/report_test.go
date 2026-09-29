package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/corn-xi/oci-artifact-stat/internal/audit"
	"github.com/corn-xi/oci-artifact-stat/internal/ui"
)

func skewedRun() audit.Run {
	return audit.Run{
		ArtifactType: "image",
		Results: []audit.Result{{
			Repository: "repo",
			Version:    "1.0.0",
			Explain: &audit.Explain{
				ByType:      map[string]int{"IMAGE": 80, "CHART": 20},
				Tags:        1629,
				VersionTags: 338,
				OtherTypes:  map[string]audit.TagStats{"CHART": {Tags: 265, Versions: 265}},
			},
		}},
	}
}

func TestHint(t *testing.T) {
	got := Hint(skewedRun())
	for _, want := range []string{"20%", "IMAGE", "100%", "CHART", "--artifact-type chart"} {
		if !strings.Contains(got, want) {
			t.Errorf("hint %q should mention %q", got, want)
		}
	}

	// Nothing to recommend, nothing said.
	quiet := skewedRun()
	quiet.Results[0].Explain.VersionTags = 1600
	if h := Hint(quiet); h != "" {
		t.Errorf("Hint() = %q, want empty when the audited type reads fine", h)
	}
}

// CI should be able to act on the suggestion without parsing prose.
func TestJSONCarriesTheSuggestion(t *testing.T) {
	var buf bytes.Buffer
	if err := JSON(&buf, skewedRun()); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Suggestion *struct {
			ArtifactType  string `json:"artifact_type"`
			AuditedRate   int    `json:"audited_parse_rate"`
			SuggestedRate int    `json:"suggested_parse_rate"`
		} `json:"suggestion"`
		Results []struct {
			Explain *struct{} `json:"explain"`
		} `json:"results"`
	}
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Suggestion == nil {
		t.Fatalf("no suggestion in:\n%s", buf.String())
	}
	if doc.Suggestion.ArtifactType != "CHART" || doc.Suggestion.AuditedRate != 20 {
		t.Errorf("suggestion = %+v", doc.Suggestion)
	}
	// The record is collected always but published only under --explain.
	if doc.Results[0].Explain != nil {
		t.Error("explain should be omitted unless the user asked for it")
	}
}

// A finding shared by many repositories is stated once. Repeating it per
// repository is what buried everything else on a registry where every
// repository is tagged the same way.
func TestDetailsCollapsesARepeatedFinding(t *testing.T) {
	run := audit.Run{}
	for i, name := range []string{"one", "two", "three", "four", "five"} {
		run.Results = append(run.Results, audit.Result{
			Repository: name,
			Status:     audit.StatusWarn,
			Findings: []audit.Finding{{
				Code:     audit.CodeNoVersionTags,
				Severity: audit.StatusWarn,
				Message:  name + ": no version-shaped tag",
			}},
		})
		_ = i
	}

	var buf bytes.Buffer
	if err := Details(&buf, run, ui.Style{}); err != nil {
		t.Fatal(err)
	}
	got := buf.String()

	if n := strings.Count(got, "no version-shaped tag"); n != 1 {
		t.Errorf("the message appeared %d times, want once:\n%s", n, got)
	}
	if !strings.Contains(got, "and 4 more with no-version-tags") {
		t.Errorf("the collapsed line should count the rest:\n%s", got)
	}
	for _, name := range []string{"two", "three", "four", "five"} {
		if !strings.Contains(got, name) {
			t.Errorf("repository %q should still be named:\n%s", name, got)
		}
	}
}

// A handful still prints in full; collapsing two lines into two lines plus a
// summary would be worse than leaving them alone.
func TestDetailsKeepsASmallGroupIntact(t *testing.T) {
	run := audit.Run{Results: []audit.Result{
		{Repository: "one", Findings: []audit.Finding{{Code: "stale-publish", Severity: audit.StatusWarn, Message: "one: stale"}}},
		{Repository: "two", Findings: []audit.Finding{{Code: "stale-publish", Severity: audit.StatusWarn, Message: "two: stale"}}},
	}}

	var buf bytes.Buffer
	if err := Details(&buf, run, ui.Style{}); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	for _, want := range []string{"one: stale", "two: stale"} {
		if !strings.Contains(got, want) {
			t.Errorf("want %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "more with") {
		t.Errorf("two findings should not be collapsed:\n%s", got)
	}
}
