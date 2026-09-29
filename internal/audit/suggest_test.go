package audit

import "testing"

// run builds a Run whose totals match the given tag counts.
func run(auditedType string, tags, versions int, others map[string]TagStats) Run {
	return Run{
		ArtifactType: auditedType,
		Results: []Result{{
			Repository: "repo",
			Explain: &Explain{
				ByType:      map[string]int{},
				Tags:        tags,
				VersionTags: versions,
				OtherTypes:  others,
			},
		}},
	}
}

func TestSuggest(t *testing.T) {
	tests := []struct {
		name      string
		run       Run
		wantType  string
		wantAudit int
		wantOther int
		wantOK    bool
	}{
		{
			// The production case: images answered from one tag in five while
			// every chart tag carried a clean version.
			name:      "landslide in favour of another type",
			run:       run("image", 1629, 338, map[string]TagStats{"CHART": {Tags: 265, Versions: 265}}),
			wantType:  "CHART",
			wantAudit: 20,
			wantOther: 100,
			wantOK:    true,
		},
		{
			name: "audited type reads well enough to be trusted",
			run:  run("image", 100, 80, map[string]TagStats{"CHART": {Tags: 50, Versions: 50}}),
		},
		{
			name: "other type is better but not markedly so",
			run:  run("image", 100, 40, map[string]TagStats{"CHART": {Tags: 50, Versions: 32}}),
		},
		{
			name: "too few tags of the other type to conclude anything",
			run:  run("image", 100, 10, map[string]TagStats{"CHART": {Tags: 4, Versions: 4}}),
		},
		{
			name: "nothing parses anywhere",
			run:  run("image", 17, 0, map[string]TagStats{"CHART": {Tags: 2}}),
		},
		{
			name: "no other types at all",
			run:  run("image", 100, 10, map[string]TagStats{}),
		},
		{
			name: "picks the best of several excluded types",
			run: run("image", 200, 20, map[string]TagStats{
				"CHART": {Tags: 100, Versions: 95},
				"CNAB":  {Tags: 40, Versions: 12},
			}),
			wantType:  "CHART",
			wantAudit: 10,
			wantOther: 95,
			wantOK:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := tt.run.Suggest()
			if ok != tt.wantOK {
				t.Fatalf("Suggest() ok = %v, want %v (got %+v)", ok, tt.wantOK, got)
			}
			if !ok {
				return
			}
			if got.Type != tt.wantType || got.AuditedRate != tt.wantAudit || got.SuggestedRate != tt.wantOther {
				t.Errorf("Suggest() = %+v, want type %s, rates %d/%d",
					got, tt.wantType, tt.wantAudit, tt.wantOther)
			}
		})
	}
}

func TestTagStatsRate(t *testing.T) {
	if got := (TagStats{}).Rate(); got != 0 {
		t.Errorf("empty Rate() = %d, want 0 and no division by zero", got)
	}
	if got := (TagStats{Tags: 8, Versions: 2}).Rate(); got != 25 {
		t.Errorf("Rate() = %d, want 25", got)
	}
}
