package audit

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/corn-xi/oci-artifact-stat/internal/registry"
)

var base = time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)

// at builds an artifact pushed `day` days after the base instant.
func at(day int, typ string, tags ...string) registry.Artifact {
	return registry.Artifact{
		Type: typ, Tags: tags,
		PushTime:    base.AddDate(0, 0, day),
		HasPushTime: true,
	}
}

type httpErr struct{ code int }

func (e *httpErr) Error() string   { return "boom" }
func (e *httpErr) StatusCode() int { return e.code }

func findingCodes(r Result) []string {
	out := make([]string, 0, len(r.Findings))
	for _, f := range r.Findings {
		out = append(out, f.Code)
	}
	return out
}

func hasCode(r Result, code string) bool {
	for _, f := range r.Findings {
		if f.Code == code {
			return true
		}
	}
	return false
}

// Each of these encodes a defect the original heuristic had. The comments
// record the old behavior so a future change cannot quietly reintroduce it.
func TestAnalyzeFixesOriginalDefects(t *testing.T) {
	t.Run("a chart pushed last does not fake an anomaly", func(t *testing.T) {
		// Was: the newest artifact was read without filtering by type, so the
		// chart's 9.9.9 became "the most recently pushed tag" and the
		// repository was flagged WARN for nothing.
		got := Analyzer{}.Analyze("charts-mixed", []registry.Artifact{
			at(2, "CHART", "9.9.9"),
			at(1, "IMAGE", "1.0.0"),
		}, nil)

		if got.Status != StatusOK {
			t.Errorf("Status = %v (%v), want OK -- a chart must not drive the image verdict",
				got.Status, findingCodes(got))
		}
		if got.Version != "1.0.0" {
			t.Errorf("Version = %q, want 1.0.0", got.Version)
		}
	})

	t.Run("a multi-tagged newest artifact no longer hides an anomaly", func(t *testing.T) {
		// Was: only Tags[0] of the newest artifact was examined. "latest"
		// does not parse as a version, so the comparison was skipped and a
		// genuine stale publish went unreported.
		got := Analyzer{}.Analyze("worker", []registry.Artifact{
			at(2, "IMAGE", "latest", "2.0.0"),
			at(1, "IMAGE", "2.5.0"),
		}, nil)

		if !hasCode(got, CodeStalePublish) {
			t.Errorf("findings = %v, want %s -- 2.0.0 was published after 2.5.0",
				findingCodes(got), CodeStalePublish)
		}
		if got.Version != "2.5.0" {
			t.Errorf("Version = %q, want 2.5.0", got.Version)
		}
	})

	t.Run("a release candidate is not reported as the version", func(t *testing.T) {
		// Was: prereleases were invisible to the parser, so 3.0.0-rc1 simply
		// outranked 2.9.0 and the tool reported an unreleased version.
		artifacts := []registry.Artifact{
			at(2, "IMAGE", "3.0.0-rc1"),
			at(1, "IMAGE", "2.9.0"),
		}
		if got := (Analyzer{}).Analyze("api", artifacts, nil); got.Version != "2.9.0" {
			t.Errorf("Version = %q, want 2.9.0 -- a release candidate is not a release", got.Version)
		}
		if got := (Analyzer{IncludePrereleases: true}).Analyze("api", artifacts, nil); got.Version != "3.0.0-rc1" {
			t.Errorf("--include-prereleases Version = %q, want 3.0.0-rc1", got.Version)
		}
	})
}

func TestAnalyze(t *testing.T) {
	tests := []struct {
		name        string
		analyzer    Analyzer
		artifacts   []registry.Artifact
		err         error
		wantStatus  Status
		wantVersion string
		wantCodes   []string
	}{
		{
			name:        "healthy repository",
			artifacts:   []registry.Artifact{at(2, "IMAGE", "2.4.1"), at(1, "IMAGE", "2.4.0")},
			wantStatus:  StatusOK,
			wantVersion: "2.4.1",
		},
		{
			name:        "stale publish",
			artifacts:   []registry.Artifact{at(2, "IMAGE", "1.9.0"), at(1, "IMAGE", "1.11.2")},
			wantStatus:  StatusWarn,
			wantVersion: "1.11.2",
			wantCodes:   []string{CodeStalePublish},
		},
		{
			// Same release, different qualifier: ordinary publishing.
			name:        "qualifier difference is not an anomaly",
			artifacts:   []registry.Artifact{at(2, "IMAGE", "2.25.0-rel-build42"), at(1, "IMAGE", "2.25.0-rel")},
			wantStatus:  StatusOK,
			wantVersion: "2.25.0",
		},
		{
			name:        "absent type is treated as an image",
			artifacts:   []registry.Artifact{at(1, "", "3.0.0")},
			wantStatus:  StatusOK,
			wantVersion: "3.0.0",
		},
		{
			name:        "no tags at all",
			artifacts:   []registry.Artifact{at(1, "IMAGE")},
			wantStatus:  StatusEmpty,
			wantVersion: "<no tags>",
		},
		{
			name:        "no version-shaped tag",
			artifacts:   []registry.Artifact{at(1, "IMAGE", "custom-build")},
			wantStatus:  StatusWarn,
			wantVersion: "custom-build",
			wantCodes:   []string{CodeNoVersionTags},
		},
		{
			name:        "only prereleases published",
			artifacts:   []registry.Artifact{at(2, "IMAGE", "3.0.0-rc2"), at(1, "IMAGE", "3.0.0-rc1")},
			wantStatus:  StatusWarn,
			wantVersion: "3.0.0-rc2",
			wantCodes:   []string{CodeOnlyPrereleases},
		},
		{
			// Raising the window can only help when it yielded nothing.
			name:        "exhausted window with no version tag",
			analyzer:    Analyzer{ArtifactWindow: 2},
			artifacts:   []registry.Artifact{at(2, "IMAGE", "latest"), at(1, "IMAGE", "nightly")},
			wantStatus:  StatusWarn,
			wantVersion: "latest",
			// Exactly one finding: the exhausted window is the actionable
			// root cause, and pairing it with no-version-tags would say the
			// same thing twice.
			wantCodes: []string{CodeWindowTruncated},
		},
		{
			// A full window that did yield versions is not suspicious: what
			// lies beyond is older than what we already have.
			name:        "exhausted window that found versions stays quiet",
			analyzer:    Analyzer{ArtifactWindow: 2},
			artifacts:   []registry.Artifact{at(2, "IMAGE", "2.0.0"), at(1, "IMAGE", "1.0.0")},
			wantStatus:  StatusOK,
			wantVersion: "2.0.0",
		},
		{
			name:        "fetch failure still produces a row",
			err:         &httpErr{code: 404},
			wantStatus:  StatusFail,
			wantVersion: "-",
			wantCodes:   []string{CodeFetchFailed},
		},
		{
			name: "missing push time downgrades to an explicit note",
			artifacts: []registry.Artifact{
				{Type: "IMAGE", Tags: []string{"1.0.0"}},
				{Type: "IMAGE", Tags: []string{"2.0.0"}},
			},
			wantStatus:  StatusWarn,
			wantVersion: "2.0.0",
			wantCodes:   []string{CodePushTimeUnavailable},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.analyzer.Analyze("repo", tt.artifacts, tt.err)

			if got.Status != tt.wantStatus {
				t.Errorf("Status = %v, want %v (findings: %v)", got.Status, tt.wantStatus, findingCodes(got))
			}
			if got.Version != tt.wantVersion {
				t.Errorf("Version = %q, want %q", got.Version, tt.wantVersion)
			}
			if len(got.Findings) != len(tt.wantCodes) {
				t.Fatalf("findings = %v, want %v", findingCodes(got), tt.wantCodes)
			}
			for _, code := range tt.wantCodes {
				if !hasCode(got, code) {
					t.Errorf("findings = %v, want %s among them", findingCodes(got), code)
				}
			}
			for _, f := range got.Findings {
				if !strings.Contains(f.Message, "repo") {
					t.Errorf("message %q should name the repository", f.Message)
				}
			}
		})
	}
}

// A suppressed finding must leave no trace at all, including on the status --
// otherwise --fail-on becomes unmanageable in CI.
func TestIgnoreSuppressesFindingAndStatus(t *testing.T) {
	artifacts := []registry.Artifact{at(2, "IMAGE", "1.9.0"), at(1, "IMAGE", "1.11.2")}

	noisy := Analyzer{}.Analyze("repo", artifacts, nil)
	if noisy.Status != StatusWarn {
		t.Fatalf("precondition: Status = %v, want WARN", noisy.Status)
	}

	quiet := Analyzer{Ignore: IgnoreSet{{Code: CodeStalePublish}}}.Analyze("repo", artifacts, nil)
	if len(quiet.Findings) != 0 {
		t.Errorf("findings = %v, want none", findingCodes(quiet))
	}
	if quiet.Status != StatusOK {
		t.Errorf("Status = %v, want OK -- a suppressed finding must not set the exit code", quiet.Status)
	}
	if quiet.Version != noisy.Version {
		t.Errorf("--ignore changed the reported version: %q vs %q", quiet.Version, noisy.Version)
	}
}

// A scoped rule must silence its own repository and no other -- the whole
// point of targeting is not to lose the signal elsewhere.
func TestIgnoreCanBeScopedToOneRepository(t *testing.T) {
	artifacts := []registry.Artifact{at(2, "IMAGE", "1.9.0"), at(1, "IMAGE", "1.11.2")}
	a := Analyzer{Ignore: IgnoreSet{{Repository: "expected", Code: CodeStalePublish}}}

	if got := a.Analyze("expected", artifacts, nil); got.Status != StatusOK {
		t.Errorf("targeted repository: Status = %v, want OK", got.Status)
	}
	if got := a.Analyze("other", artifacts, nil); got.Status != StatusWarn {
		t.Errorf("untargeted repository: Status = %v, want WARN -- the rule must not leak", got.Status)
	}
}

func TestAnalyzeFetchFailureReportsStatusCode(t *testing.T) {
	got := Analyzer{}.Analyze("legacy-app", nil, &httpErr{code: 503})
	if !strings.Contains(got.Findings[0].Message, "HTTP 503") {
		t.Errorf("message = %q, want the status code", got.Findings[0].Message)
	}
	// Without a status, whatever the backend said is the only useful part of
	// the message, and must survive: a bare "HTTP 000" threw it away, which
	// on an OCI registry meant losing "DENIED" or "name unknown".
	noCode := Analyzer{}.Analyze("legacy-app", nil, &httpErr{code: 0})
	if !strings.Contains(noCode.Findings[0].Message, "boom") {
		t.Errorf("message = %q, want the backend's own words when there is no status",
			noCode.Findings[0].Message)
	}
}

func TestAnalyzeRawTags(t *testing.T) {
	artifacts := []registry.Artifact{at(1, "IMAGE", "2.18.0-rel-build42")}

	friendly := Analyzer{}.Analyze("repo", artifacts, nil)
	if friendly.Version != "2.18.0" {
		t.Errorf("default Version = %q, want the parsed version", friendly.Version)
	}
	raw := Analyzer{RawTags: true}.Analyze("repo", artifacts, nil)
	if raw.Version != "2.18.0-rel-build42" {
		t.Errorf("--raw-tags Version = %q, want the tag as pushed", raw.Version)
	}
	if raw.RawTag != friendly.RawTag {
		t.Errorf("RawTag should not depend on display mode: %q vs %q", raw.RawTag, friendly.RawTag)
	}
}

func TestStalePublishMessageNamesBothVersionsAndTimes(t *testing.T) {
	got := Analyzer{}.Analyze("worker", []registry.Artifact{
		at(5, "IMAGE", "1.9.0"),
		at(1, "IMAGE", "1.11.2"),
	}, nil)

	msg := got.Findings[0].Message
	for _, want := range []string{"1.9.0", "1.11.2", "2026-09-06", "2026-09-02"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q should mention %q", msg, want)
		}
	}
}

// When version order does not follow push order, a full window is a real
// blind spot: a higher version may sit just past it, and being older is no
// longer evidence of being lower. The caveat rides on the stale-publish
// finding rather than becoming a second one.
func TestStalePublishMentionsAFullWindow(t *testing.T) {
	artifacts := []registry.Artifact{
		at(3, "IMAGE", "1.9.0"),
		at(2, "IMAGE", "1.11.2"),
		at(1, "IMAGE", "untagged-meta"),
	}

	full := Analyzer{ArtifactWindow: 3}.Analyze("repo", artifacts, nil)
	if len(full.Findings) != 1 || full.Findings[0].Code != CodeStalePublish {
		t.Fatalf("findings = %v, want exactly one stale-publish", findingCodes(full))
	}
	if !strings.Contains(full.Findings[0].Message, "raise --artifact-window") {
		t.Errorf("a full window should add the caveat, got: %s", full.Findings[0].Message)
	}
	if !strings.Contains(full.Findings[0].Message, "Only 2 of the 3 artifacts") {
		t.Errorf("the caveat should quantify the usable window, got: %s", full.Findings[0].Message)
	}

	roomy := Analyzer{ArtifactWindow: 20}.Analyze("repo", artifacts, nil)
	if strings.Contains(roomy.Findings[0].Message, "raise --artifact-window") {
		t.Errorf("a window with room to spare needs no caveat, got: %s", roomy.Findings[0].Message)
	}
}

// Versions increasing with push time means nothing beyond the window can be
// higher, so a full window raises no doubt at all.
func TestFullWindowIsQuietWhenOrderIsMonotonic(t *testing.T) {
	got := Analyzer{ArtifactWindow: 2}.Analyze("repo", []registry.Artifact{
		at(2, "IMAGE", "2.0.0"),
		at(1, "IMAGE", "1.0.0"),
	}, nil)
	if len(got.Findings) != 0 {
		t.Errorf("findings = %v, want none", findingCodes(got))
	}
}

// Modelled on a real registry: the pipeline stopped giving images a
// parseable version tag, while the chart beside them kept one. The image
// answer silently went stale, and nothing in the report said why.
func TestTypeFilterHidingRecencyIsReported(t *testing.T) {
	artifacts := []registry.Artifact{
		at(17, "CHART", "2.12.0-rel"),
		at(17, "IMAGE", "custom-tag-meta-2-12-0-rel-cs-ko-d1faba78"), // unparseable
		at(5, "IMAGE", "2.12.0-rel-cs-ko"),
		at(4, "IMAGE", "3.0.0-rel-cs-ko"),
	}
	got := Analyzer{}.Analyze("cs-ko-verification-result", artifacts, nil)

	if got.Version != "3.0.0" {
		t.Errorf("Version = %q, want 3.0.0 -- the highest readable IMAGE version", got.Version)
	}
	if !hasCode(got, CodeVersionOnOtherType) {
		t.Fatalf("findings = %v, want %s -- the chart holds a newer version than any readable image tag",
			findingCodes(got), CodeVersionOnOtherType)
	}
	for _, f := range got.Findings {
		if f.Code != CodeVersionOnOtherType {
			continue
		}
		for _, want := range []string{"2.12.0-rel", "CHART", "--artifact-type chart"} {
			if !strings.Contains(f.Message, want) {
				t.Errorf("message should mention %q: %s", want, f.Message)
			}
		}
	}
}

// The common shape on a real registry: newer images carry an unparseable tag
// while the chart keeps a clean one, but both name the SAME version. Nothing
// is stale and nothing needs saying. Warning here fired on 21 of 21
// production repositories, 20 of them pointlessly.
func TestRecencyLossWithAgreeingVersionsIsQuiet(t *testing.T) {
	got := Analyzer{}.Analyze("freezing-funds", []registry.Artifact{
		at(17, "CHART", "2.24.0-rel"),
		at(17, "IMAGE", "custom-tag-meta-2-24-0-rel-a1b2c3"), // unparseable
		at(10, "IMAGE", "2.24.0-rel-svc"),
	}, nil)

	if got.Version != "2.24.0" {
		t.Errorf("Version = %q, want 2.24.0", got.Version)
	}
	if len(got.Findings) != 0 {
		t.Errorf("findings = %v, want none -- both types name the same version", findingCodes(got))
	}
}

// Chart and image published together, both readable: nothing is being hidden,
// so nothing is said. Warning here would fire on every co-published
// repository and drown the real case above.
func TestLockstepChartAndImageAreQuiet(t *testing.T) {
	got := Analyzer{}.Analyze("repo", []registry.Artifact{
		at(2, "CHART", "2.0.0-rel"),
		at(2, "IMAGE", "2.0.0-rel"),
		at(1, "CHART", "1.0.0-rel"),
		at(1, "IMAGE", "1.0.0-rel"),
	}, nil)
	if len(got.Findings) != 0 {
		t.Errorf("findings = %v, want none", findingCodes(got))
	}
}

// Independent version lines under one path are not a recency problem either.
func TestUnrelatedChartVersionIsQuiet(t *testing.T) {
	got := Analyzer{}.Analyze("repo", []registry.Artifact{
		at(2, "CHART", "9.9.9"),
		at(1, "IMAGE", "1.0.0"),
	}, nil)
	if len(got.Findings) != 0 {
		t.Errorf("findings = %v, want none -- a chart on its own version line says nothing about the image",
			findingCodes(got))
	}
}

func TestArtifactTypeSelectsWhatIsAudited(t *testing.T) {
	artifacts := []registry.Artifact{
		at(2, "CHART", "2.12.0-rel"),
		at(1, "IMAGE", "1.0.0-rel"),
	}
	if got := (Analyzer{}).Analyze("repo", artifacts, nil); got.Version != "1.0.0" {
		t.Errorf("default Version = %q, want the image version", got.Version)
	}
	if got := (Analyzer{ArtifactType: "chart"}).Analyze("repo", artifacts, nil); got.Version != "2.12.0" {
		t.Errorf("--artifact-type chart Version = %q, want the chart version", got.Version)
	}
	// Types are never mixed, so each run sees only its own.
	if got := (Analyzer{ArtifactType: "chart"}).Analyze("repo", artifacts, nil); got.RawTag != "2.12.0-rel" {
		t.Errorf("RawTag = %q, want the chart's own tag", got.RawTag)
	}
}

func TestSummaryCountsEveryRow(t *testing.T) {
	var s Summary
	for _, st := range []Status{StatusOK, StatusOK, StatusWarn, StatusFail, StatusEmpty} {
		s.add(st)
	}
	if s.Total() != 5 {
		t.Errorf("Total() = %d, want 5 -- the counts must sum to the rows printed", s.Total())
	}
}

// A backend that never has push times states that once for the run, rather
// than raising a finding against every repository it reads. On a generic OCI
// registry the old behaviour warned on every row of every run, which made
// --fail-on warn unusable there.
func TestBackendWithoutPushTimesReportsOnceNotPerRepository(t *testing.T) {
	artifacts := []registry.Artifact{
		{Type: "IMAGE", Tags: []string{"1.0.0"}},
		{Type: "IMAGE", Tags: []string{"2.0.0"}},
	}

	expected := Analyzer{}.Analyze("repo", artifacts, nil)
	if !hasCode(expected, CodePushTimeUnavailable) {
		t.Fatal("precondition: a backend that should have push times still reports their absence")
	}

	known := Analyzer{NoPushTimes: true}.Analyze("repo", artifacts, nil)
	if len(known.Findings) != 0 {
		t.Errorf("findings = %v, want none -- the limit belongs to the registry", findingCodes(known))
	}
	if known.Status != StatusOK {
		t.Errorf("Status = %v, want OK", known.Status)
	}
	if known.Version != "2.0.0" {
		t.Errorf("Version = %q, want the highest tag regardless", known.Version)
	}
}

// A slow registry looks exactly like a broken one, and the remedy is a flag
// rather than a bug report, so timeouts are counted apart.
func TestRunTimedOutCountsOnlyTimeouts(t *testing.T) {
	run := Run{Results: []Result{
		{Repository: "slow", Err: &url.Error{Op: "Get", Err: timeoutErr{}}},
		{Repository: "deadline", Err: fmt.Errorf("fetching: %w", context.DeadlineExceeded)},
		{Repository: "denied", Err: errors.New("DENIED")},
		{Repository: "fine"},
	}}
	if got := run.TimedOut(); got != 2 {
		t.Errorf("TimedOut() = %d, want 2", got)
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return false }

// wrappedErr mimics a backend error: a label, a status of zero, and the real
// cause underneath.
type wrappedErr struct {
	label string
	inner error
}

func (e *wrappedErr) Error() string   { return e.label + ": no response: " + e.inner.Error() }
func (e *wrappedErr) StatusCode() int { return 0 }
func (e *wrappedErr) Unwrap() error   { return e.inner }

// The message already names the repository, so the error must not name it
// again, and *url.Error must not repeat the whole request URL.
func TestFetchFailureDoesNotRepeatItself(t *testing.T) {
	err := &wrappedErr{
		label: "'svc' artifacts",
		inner: &url.Error{
			Op:  "Get",
			URL: "http://registry.example.com/api/v2.0/projects/p/repositories/svc/artifacts?page_size=20",
			Err: errors.New("context deadline exceeded"),
		},
	}

	msg := Analyzer{}.Analyze("svc", nil, err).Findings[0].Message

	if want := "'svc': artifact fetch failed, context deadline exceeded"; msg != want {
		t.Errorf("message = %q, want %q", msg, want)
	}
	if strings.Count(msg, "svc") != 1 {
		t.Errorf("the repository is named %d times: %s", strings.Count(msg, "svc"), msg)
	}
	if strings.Contains(msg, "http://") {
		t.Errorf("the request URL should not be repeated: %s", msg)
	}
}
