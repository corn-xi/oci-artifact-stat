package audit

import (
	"context"
	"strings"
	"testing"

	"github.com/corn-xi/oci-artifact-stat/internal/registry"
)

// skewed reproduces the shape found in a production registry: every release
// publishes a chart with a clean version and an image whose tag does not
// parse, so images answer from a minority of their tags.
func skewed(noise int) []registry.Artifact {
	arts := []registry.Artifact{
		at(17, "CHART", "2.24.0-rel"),
		at(17, "IMAGE", "custom-tag-meta-2-24-0-rel-a1b2c3"),
	}
	// Release history: the chart keeps a readable version every time, the
	// image only sometimes.
	for i := 0; i < 3; i++ {
		arts = append(arts,
			at(10-i, "CHART", "2.2"+string(rune('0'+i))+".0-rel"),
			at(10-i, "IMAGE", "custom-tag-meta-2-2"+string(rune('0'+i))+"-0-rel"),
		)
	}
	// The one image tag that does parse, so images are not simply empty.
	arts = append(arts, at(1, "IMAGE", "2.20.0-rel-svc"))
	// Build metadata artifacts, which carry nothing useful either way.
	for i := 0; i < noise; i++ {
		arts = append(arts, at(i, "IMAGE", "meta-74d69d5c_deadbeef"))
	}
	return arts
}

func runAll(t *testing.T, a Analyzer, artifacts []registry.Artifact, repos int) Run {
	t.Helper()
	list := make([]registry.Repository, repos)
	for i := range list {
		list[i] = registry.Repository{Name: "svc"}
	}
	return a.RunAll(context.Background(), registry.Scope{Name: "scope"}, list, 4,
		func(context.Context, registry.Repository) ([]registry.Artifact, error) {
			return artifacts, nil
		})
}

func TestRunAllAutoSelectsTheReadableType(t *testing.T) {
	// Default: the type is undecided and the run settles it.
	got := runAll(t, Analyzer{}, skewed(4), 3)

	if got.ArtifactType != "chart" {
		t.Errorf("ArtifactType = %q, want chart", got.ArtifactType)
	}
	if got.Auto == nil {
		t.Fatal("Auto should record that the type was chosen, not given")
	}
	if got.Auto.SuggestedRate != 100 || got.Auto.AuditedRate >= 50 {
		t.Errorf("Auto = %+v, want a landslide in favour of charts", got.Auto)
	}
	if got.Summary.OK != 3 {
		t.Errorf("summary = %+v, want every repository OK once read as charts", got.Summary)
	}
	// Lower case, so the machine-readable output does not depend on which
	// path set the type.
	if strings.ToLower(got.ArtifactType) != got.ArtifactType {
		t.Errorf("ArtifactType = %q, want it lower-cased", got.ArtifactType)
	}
}

func TestRunAllRespectsAnExplicitType(t *testing.T) {
	got := runAll(t, Analyzer{ArtifactType: "image"}, skewed(4), 3)

	if got.ArtifactType != "image" {
		t.Errorf("ArtifactType = %q, want the type that was asked for", got.ArtifactType)
	}
	if got.Auto != nil {
		t.Error("an explicit type must not be overridden")
	}
	// It still says what it noticed.
	if _, ok := got.Suggest(); !ok {
		t.Error("the skew should still be reported as a suggestion")
	}
}

// Nothing to switch to, so nothing changes.
func TestRunAllAutoStaysOnImagesWhenTheyRead(t *testing.T) {
	got := runAll(t, Analyzer{}, []registry.Artifact{
		at(2, "IMAGE", "2.0.0-rel"),
		at(1, "IMAGE", "1.0.0-rel"),
	}, 2)

	if got.ArtifactType != registry.TypeImage {
		t.Errorf("ArtifactType = %q, want image", got.ArtifactType)
	}
	if got.Auto != nil {
		t.Errorf("Auto = %+v, want no switch", got.Auto)
	}
}

// Row order must survive the second analysis pass.
func TestRunAllKeepsRepositoryOrderAfterAutoSwitch(t *testing.T) {
	repos := []registry.Repository{{Name: "alpha"}, {Name: "beta"}, {Name: "gamma"}}
	got := Analyzer{}.RunAll(context.Background(), registry.Scope{}, repos, 3,
		func(_ context.Context, r registry.Repository) ([]registry.Artifact, error) {
			return skewed(4), nil
		})

	if got.Auto == nil {
		t.Fatal("precondition: the run should have switched type")
	}
	for i, want := range []string{"alpha", "beta", "gamma"} {
		if got.Results[i].Repository != want {
			t.Errorf("row %d = %q, want %q", i, got.Results[i].Repository, want)
		}
	}
}
