package cli

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite golden files")

// Fixtures cover each STATUS the table can show plus the awkward shapes: a
// chart sharing a repository path, a name with slashes, and a repository
// published only under a qualified tag.
var fixtures = []struct {
	name      string
	artifacts string // nil body means the endpoint 404s
}{
	{"api-gateway", `[{"type":"IMAGE","push_time":"2026-09-01T10:00:00Z","tags":[{"name":"2.4.1"}]},
	                  {"type":"IMAGE","push_time":"2026-08-01T10:00:00Z","tags":[{"name":"2.4.0"}]}]`},
	{"charts-mixed", `[{"type":"CHART","push_time":"2026-09-02T10:00:00Z","tags":[{"name":"9.9.9"}]},
	                   {"type":"IMAGE","push_time":"2026-08-02T10:00:00Z","tags":[{"name":"1.0.0"}]}]`},
	{"group/sub/repo", `[{"type":"IMAGE","push_time":"2026-09-03T10:00:00Z","tags":[{"name":"3.2.1-rel-build7"},{"name":"3.2.1"}]}]`},
	{"legacy-app", ""},
	{"no-tags", `[{"type":"IMAGE","push_time":"2026-09-04T10:00:00Z","tags":null}]`},
	{"qualified-only", `[{"type":"IMAGE","push_time":"2026-09-05T10:00:00Z","tags":[{"name":"2.18.0-rel-build42"}]}]`},
	{"worker-service", `[{"type":"IMAGE","push_time":"2026-09-06T10:00:00Z","tags":[{"name":"1.9.0"}]},
	                     {"type":"IMAGE","push_time":"2026-08-06T10:00:00Z","tags":[{"name":"1.11.2"},{"name":"1.11.2-rel-build42"}]}]`},
	// A release candidate published after the last release: the default view
	// must report the release, not the candidate.
	{"next-release", `[{"type":"IMAGE","push_time":"2026-09-07T10:00:00Z","tags":[{"name":"3.0.0-rc1"}]},
	                   {"type":"IMAGE","push_time":"2026-08-07T10:00:00Z","tags":[{"name":"2.9.0"}]}]`},
	// Nothing released at all -- reporting the candidate beats reporting
	// nothing, as long as the report says which it is.
	{"rc-only", `[{"type":"IMAGE","push_time":"2026-09-08T10:00:00Z","tags":[{"name":"4.0.0-rc2"}]},
	              {"type":"IMAGE","push_time":"2026-08-08T10:00:00Z","tags":[{"name":"4.0.0-rc1"}]}]`},
	// The shape seen in a real registry: the pipeline stopped giving images a
	// parseable tag, while the chart beside them kept the version.
	{"chart-versioned", `[{"type":"CHART","push_time":"2026-09-18T08:14:19Z","tags":[{"name":"2.12.0-rel"}]},
	                      {"type":"IMAGE","push_time":"2026-09-18T08:14:11Z","tags":[{"name":"custom-tag-meta-2-12-0-rel-d1faba78"}]},
	                      {"type":"IMAGE","push_time":"2026-08-25T14:30:41Z","tags":[{"name":"2.12.0-rel-svc"}]},
	                      {"type":"IMAGE","push_time":"2026-08-25T14:06:30Z","tags":[{"name":"3.0.0-rel-svc"}]}]`},
	// The same pipeline shape, but both types name the same version. This is
	// the common case on a real registry and must stay quiet.
	{"chart-agreeing", `[{"type":"CHART","push_time":"2026-09-18T08:38:07Z","tags":[{"name":"2.24.0-rel"}]},
	                     {"type":"IMAGE","push_time":"2026-09-18T08:38:01Z","tags":[{"name":"custom-tag-meta-2-24-0-rel-a1b2c3"}]},
	                     {"type":"IMAGE","push_time":"2026-09-11T10:00:00Z","tags":[{"name":"2.24.0-rel-svc"}]}]`},
}

// fakeHarbor serves just enough of the API, with a little jitter so that
// request completion order varies between runs.
func fakeHarbor(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(time.Duration(rand.Intn(3000)) * time.Microsecond)
		path := r.URL.Path
		w.Header().Set("Content-Type", "application/json")

		switch {
		// Answered without credentials by a real Harbor; this is how the
		// backend is detected.
		case path == "/api/v2.0/systeminfo":
			fmt.Fprint(w, `{"harbor_version":"v2.10.0"}`)

		case path == "/api/v2.0/projects/7":
			fmt.Fprint(w, `{"project_id":7,"name":"platform","repo_count":7,"metadata":{"public":"false"}}`)

		case path == "/api/v2.0/projects" && r.URL.Query().Get("name") != "":
			fmt.Fprint(w, `[{"project_id":7,"name":"platform","repo_count":7,"metadata":{"public":"false"}}]`)

		case path == "/api/v2.0/projects":
			w.Header().Set("X-Total-Count", "2")
			fmt.Fprint(w, `[{"project_id":7,"name":"platform","repo_count":7,"metadata":{"public":"false"}},
			                {"project_id":12,"name":"payments","repo_count":3,"metadata":{"public":"true"}}]`)

		case path == "/api/v2.0/repositories":
			names := make([]string, 0, len(fixtures)+1)
			for _, f := range fixtures {
				names = append(names, fmt.Sprintf(`{"name":"platform/%s"}`, f.name))
			}
			// Must be dropped by the client-side prefix filter.
			names = append(names, `{"name":"other-project/intruder"}`)
			w.Header().Set("X-Total-Count", fmt.Sprint(len(names)))
			fmt.Fprintf(w, "[%s]", strings.Join(names, ","))

		case strings.HasSuffix(path, "/artifacts"):
			repo := strings.TrimSuffix(strings.TrimPrefix(path, "/api/v2.0/projects/platform/repositories/"), "/artifacts")
			for _, f := range fixtures {
				if f.name == repo {
					if f.artifacts == "" {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					fmt.Fprint(w, f.artifacts)
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// runCLI executes the command against the fake registry and returns its
// streams and exit status.
func runCLI(t *testing.T, srv *httptest.Server, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	t.Setenv("OCI_ARTIFACT_STAT_URL", srv.URL)
	t.Setenv("OCI_ARTIFACT_STAT_USER", "tester")
	t.Setenv("OCI_ARTIFACT_STAT_PASSWORD", "secret")
	t.Setenv("HARBOR_URL", "")
	t.Setenv("HARBOR_USER", "")
	t.Setenv("HARBOR_PASSWORD", "")
	t.Setenv("NO_COLOR", "1")

	var out, errBuf bytes.Buffer
	code = Run(args, strings.NewReader(""), &out, &errBuf)
	return out.String(), errBuf.String(), code
}

func TestTableOutputMatchesGolden(t *testing.T) {
	stdout, _, code := runCLI(t, fakeHarbor(t), "platform")
	if code != exitFailures {
		t.Errorf("exit = %d, want %d (one repository fails to fetch)", code, exitFailures)
	}
	compareGolden(t, "table.golden", stdout)
}

func TestRawTagsOutputMatchesGolden(t *testing.T) {
	stdout, _, _ := runCLI(t, fakeHarbor(t), "--raw-tags", "platform")
	compareGolden(t, "raw-tags.golden", stdout)
	if !strings.Contains(stdout, "2.18.0-rel-build42") {
		t.Error("--raw-tags should show the tag exactly as pushed")
	}
}

func TestListScopesMatchesGolden(t *testing.T) {
	stdout, _, code := runCLI(t, fakeHarbor(t), "--list-scopes")
	if code != exitOK {
		t.Errorf("exit = %d, want 0", code)
	}
	compareGolden(t, "list-scopes.golden", stdout)
}

// The older spelling keeps working, but is no longer advertised.
func TestListProjectsIsAcceptedButUndocumented(t *testing.T) {
	srv := fakeHarbor(t)
	legacy, _, code := runCLI(t, srv, "--list-projects")
	if code != exitOK {
		t.Fatalf("exit = %d, want 0", code)
	}
	current, _, _ := runCLI(t, srv, "--list-scopes")
	if legacy != current {
		t.Error("--list-projects should behave exactly like --list-scopes")
	}

	var help, errBuf bytes.Buffer
	Run([]string{"--help"}, strings.NewReader(""), &help, &errBuf)
	if strings.Contains(help.String(), "--list-projects") {
		t.Error("--list-projects should not appear in --help")
	}
}

// Both earlier generations of variable names still work, each announcing
// itself once. The tool was harbor-image-stat, then oci-image-stat.
func TestLegacyEnvNamesAreHonoredAndAnnounced(t *testing.T) {
	for _, gen := range []struct{ name, url, user, password string }{
		{"harbor", "HARBOR_URL", "HARBOR_USER", "HARBOR_PASSWORD"},
		{"oci-image-stat", "OCI_IMAGE_STAT_URL", "OCI_IMAGE_STAT_USER", "OCI_IMAGE_STAT_PASSWORD"},
	} {
		t.Run(gen.name, func(t *testing.T) {
			srv := fakeHarbor(t)
			for _, unset := range []string{
				"OCI_ARTIFACT_STAT_URL", "OCI_ARTIFACT_STAT_USER", "OCI_ARTIFACT_STAT_PASSWORD",
				"OCI_IMAGE_STAT_URL", "OCI_IMAGE_STAT_USER", "OCI_IMAGE_STAT_PASSWORD",
				"HARBOR_URL", "HARBOR_USER", "HARBOR_PASSWORD",
			} {
				t.Setenv(unset, "")
			}
			t.Setenv(gen.url, srv.URL)
			t.Setenv(gen.user, "tester")
			t.Setenv(gen.password, "secret")
			t.Setenv("NO_COLOR", "1")

			var out, errBuf bytes.Buffer
			Run([]string{"platform"}, strings.NewReader(""), &out, &errBuf)

			stderr := errBuf.String()
			notice := gen.url + " is deprecated"
			if n := strings.Count(stderr, notice); n != 1 {
				t.Errorf("notice %q appeared %d times, want exactly 1:\n%s", notice, n, stderr)
			}
			if !strings.Contains(stderr, "OCI_ARTIFACT_STAT_URL") {
				t.Error("the notice should name the current variable")
			}
			if !strings.Contains(out.String(), "api-gateway") {
				t.Error("the run should still work through the legacy variables")
			}
		})
	}
}

// tableRow returns the VER. and STATUS cells for a repository, so assertions
// do not depend on the column padding.
func tableRow(t *testing.T, output, repo string) (version, status string) {
	t.Helper()
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Split(line, "|")
		if len(fields) != 3 || strings.TrimSpace(fields[0]) != repo {
			continue
		}
		return strings.TrimSpace(fields[1]), strings.TrimSpace(fields[2])
	}
	t.Fatalf("no row for %q in:\n%s", repo, output)
	return "", ""
}

func TestPrereleaseHandling(t *testing.T) {
	srv := fakeHarbor(t)

	byDefault, _, _ := runCLI(t, srv, "platform")
	if v, _ := tableRow(t, byDefault, "next-release"); v != "2.9.0" {
		t.Errorf("next-release = %q by default, want 2.9.0 -- a candidate is not a release", v)
	}
	// Nothing released, so the candidate is reported -- and labelled.
	if v, status := tableRow(t, byDefault, "rc-only"); v != "4.0.0-rc2" || status != "WARN" {
		t.Errorf("rc-only = %q/%s, want 4.0.0-rc2/WARN", v, status)
	}
	if !strings.Contains(byDefault, "no released version") {
		t.Error("a repository with only prereleases should say so in the details")
	}

	included, _, _ := runCLI(t, srv, "--include-prereleases", "platform")
	if v, _ := tableRow(t, included, "next-release"); v != "3.0.0-rc1" {
		t.Errorf("next-release = %q with --include-prereleases, want 3.0.0-rc1", v)
	}
	if _, status := tableRow(t, included, "rc-only"); status != "OK" {
		t.Errorf("rc-only status = %s with --include-prereleases, want OK", status)
	}
}

// Defect A, visible end to end: a chart pushed after the newest image used to
// make the repository WARN for nothing.
func TestChartDoesNotFakeAnAnomaly(t *testing.T) {
	stdout, _, _ := runCLI(t, fakeHarbor(t), "platform")
	v, status := tableRow(t, stdout, "charts-mixed")
	if v != "1.0.0" || status != "OK" {
		t.Errorf("charts-mixed = %q/%s, want 1.0.0/OK -- a Helm chart must not drive the image verdict", v, status)
	}
}

func TestIgnoreSuppressesFindingsAndExitCode(t *testing.T) {
	srv := fakeHarbor(t)

	before, _, _ := runCLI(t, srv, "--fail-on", "warn", "--ignore", "fetch-failed", "platform")
	if !strings.Contains(before, "stale-publish") && !strings.Contains(before, "stale or duplicate") {
		t.Fatal("precondition: the stale-publish finding should be present")
	}

	_, _, code := runCLI(t, srv,
		"--fail-on", "warn",
		"--ignore", "stale-publish,only-prereleases,no-version-tags",
		"--ignore", "fetch-failed,version-on-other-type",
		"platform")
	if code != exitOK {
		t.Errorf("exit = %d, want 0 -- every finding was suppressed", code)
	}

	_, stderr, usageCode := runCLI(t, srv, "--ignore", "no-such-code", "platform")
	if usageCode != exitUsage {
		t.Errorf("exit = %d, want %d for an unknown --ignore code", usageCode, exitUsage)
	}
	if !strings.Contains(stderr, "invalid --ignore") {
		t.Errorf("stderr = %q, want it to reject the unknown code", stderr)
	}
}

// Row order must follow repository order, not whichever request finished
// first -- the reason results are written by index rather than appended.
func TestRowOrderIsDeterministicUnderConcurrency(t *testing.T) {
	srv := fakeHarbor(t)
	first, _, _ := runCLI(t, srv, "--concurrency", "8", "platform")
	for i := 0; i < 6; i++ {
		got, _, _ := runCLI(t, srv, "--concurrency", "8", "platform")
		if got != first {
			t.Fatalf("run %d differs from the first:\n%s", i+2, diffLines(first, got))
		}
	}
	// A serial run must agree with the concurrent one too.
	serial, _, _ := runCLI(t, srv, "--concurrency", "1", "platform")
	if serial != first {
		t.Errorf("serial run differs from concurrent:\n%s", diffLines(first, serial))
	}
}

func TestJSONOutputIsValidAndOwnsStdout(t *testing.T) {
	stdout, stderr, _ := runCLI(t, fakeHarbor(t), "--output", "json", "platform")

	var doc struct {
		SchemaVersion int    `json:"schema_version"`
		ArtifactType  string `json:"artifact_type"`
		Scope         struct {
			ID   int    `json:"id"`
			Name string `json:"name"`
		} `json:"scope"`
		Results []struct {
			Repository string `json:"repository"`
			Version    string `json:"version"`
			Status     string `json:"status"`
			Findings   []struct {
				Code string `json:"code"`
			} `json:"findings"`
		} `json:"results"`
		Summary struct{ Total, OK, Warn, Fail, Empty int } `json:"summary"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("stdout is not valid JSON (%v); it must carry the document alone:\n%s", err, stdout)
	}
	if doc.ArtifactType != "image" {
		t.Errorf("artifact_type = %q, want image -- a consumer cannot otherwise tell the views apart", doc.ArtifactType)
	}
	if doc.SchemaVersion != 1 || doc.Scope.Name != "platform" {
		t.Errorf("unexpected header: %+v", doc)
	}
	if doc.Summary.Total != len(doc.Results) || doc.Summary.Total != len(fixtures) {
		t.Errorf("summary total %d, results %d, fixtures %d -- all three must agree",
			doc.Summary.Total, len(doc.Results), len(fixtures))
	}
	// Progress chatter must have moved out of the way.
	if strings.Contains(stdout, "[INFO]") {
		t.Error("stdout contains log output; it would break any JSON consumer")
	}
	if !strings.Contains(stderr, "Fetching repository list") {
		t.Error("progress should still be reported, on stderr")
	}
}

func TestCSVOutput(t *testing.T) {
	stdout, _, _ := runCLI(t, fakeHarbor(t), "--output", "csv", "platform")
	rows, err := csv.NewReader(strings.NewReader(stdout)).ReadAll()
	if err != nil {
		t.Fatalf("stdout is not valid CSV: %v", err)
	}
	if len(rows) != len(fixtures)+1 {
		t.Fatalf("got %d rows, want %d plus a header", len(rows), len(fixtures))
	}
	// Rows from several runs get concatenated into one file, so each must
	// carry the scope and the type it came from.
	want := []string{"scope", "repository", "artifact_type", "version", "raw_tag", "status", "findings"}
	if strings.Join(rows[0], ",") != strings.Join(want, ",") {
		t.Errorf("header = %v, want %v", rows[0], want)
	}
	if rows[1][0] != "platform" {
		t.Errorf("first data row scope = %q, want platform", rows[1][0])
	}
}

// A scoped rule silences one repository without weakening the check anywhere
// else -- the reason to prefer it over a blanket --ignore.
func TestScopedIgnore(t *testing.T) {
	srv := fakeHarbor(t)

	out := mustRun(t, srv, "--ignore", "worker-service:stale-publish", "platform")
	if _, status := tableRow(t, out, "worker-service"); status != "OK" {
		t.Errorf("worker-service = %s, want OK -- its finding was suppressed", status)
	}
	if _, status := tableRow(t, out, "rc-only"); status != "WARN" {
		t.Errorf("rc-only = %s, want WARN -- a scoped rule must not leak", status)
	}

	_, stderr, code := runCLI(t, srv, "--ignore", "worker-service:no-such-code", "platform")
	if code != exitUsage || !strings.Contains(stderr, "invalid --ignore code") {
		t.Errorf("bad scoped code: exit = %d, stderr = %q", code, stderr)
	}
}

func mustRun(t *testing.T, srv *httptest.Server, args ...string) string {
	t.Helper()
	stdout, _, _ := runCLI(t, srv, args...)
	return stdout
}

// End to end: the default run reports a stale image version and says the
// charts know better; --artifact-type chart then reads them.
func TestArtifactTypeAndTheRecencyWarning(t *testing.T) {
	srv := fakeHarbor(t)

	byImage := mustRun(t, srv, "platform")
	v, status := tableRow(t, byImage, "chart-versioned")
	if v != "3.0.0" || status != "WARN" {
		t.Errorf("chart-versioned = %q/%s, want 3.0.0/WARN", v, status)
	}
	// Same pipeline shape, agreeing versions: nothing to report. This is the
	// majority case in production and warning on it made the check useless.
	if v, status := tableRow(t, byImage, "chart-agreeing"); v != "2.24.0" || status != "OK" {
		t.Errorf("chart-agreeing = %q/%s, want 2.24.0/OK", v, status)
	}
	if !strings.Contains(byImage, "--artifact-type chart") {
		t.Error("the report should point at the flag that reads the newer versions")
	}

	byChart := mustRun(t, srv, "--artifact-type", "chart", "platform")
	if v, _ := tableRow(t, byChart, "chart-versioned"); v != "2.12.0" {
		t.Errorf("--artifact-type chart = %q, want 2.12.0", v)
	}
	// The table has to say which view it is; two runs of one scope are
	// otherwise indistinguishable on screen.
	if !strings.Contains(byChart, "CHART ") {
		t.Error("the chart run should head its first column CHART")
	}
	// Repositories with no artifact of that type report EMPTY, not a
	// silently borrowed image version.
	if v, status := tableRow(t, byChart, "api-gateway"); status != "EMPTY" {
		t.Errorf("api-gateway under --artifact-type chart = %q/%s, want EMPTY", v, status)
	}

	_, stderr, code := runCLI(t, srv, "--artifact-type", "", "platform")
	if code != exitUsage || !strings.Contains(stderr, "invalid --artifact-type") {
		t.Errorf("empty --artifact-type: exit = %d, stderr = %q", code, stderr)
	}
}

// --explain exists so a user on an unfamiliar registry can check the tool's
// view against reality in one command, rather than reverse-engineering it
// from the report.
func TestExplain(t *testing.T) {
	srv := fakeHarbor(t)

	plain := mustRun(t, srv, "platform")
	if strings.Contains(plain, "Explain:") {
		t.Error("the explain block should only appear when asked for")
	}

	out := mustRun(t, srv, "--explain", "platform")
	if !strings.Contains(out, "Explain:") {
		t.Fatalf("no explain summary:\n%s", out)
	}
	for _, want := range []string{"repositories", "artifacts", "parsed as versions"} {
		if !strings.Contains(out, want) {
			t.Errorf("the summary should mention %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "repositorys") {
		t.Error("plural of repository is repositories")
	}

	// Only repositories whose reading might be wrong get a line; a scope of
	// uniform repositories would otherwise bury the interesting ones.
	if !strings.Contains(out, "charts-mixed:") {
		t.Error("a repository with versions on an excluded type should be listed")
	}
	if strings.Contains(out, "api-gateway:") {
		t.Error("an unremarkable repository should not be listed")
	}

	// The same record has to survive into JSON, which is what gets pasted
	// into a bug report.
	jsonOut, _, _ := runCLI(t, srv, "--explain", "-o", "json", "platform")
	var doc struct {
		Results []struct {
			Repository string `json:"repository"`
			Explain    *struct {
				Artifacts   int            `json:"artifacts"`
				ByType      map[string]int `json:"by_type"`
				VersionTags int            `json:"version_tags"`
			} `json:"explain"`
		} `json:"results"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &doc); err != nil {
		t.Fatalf("explain broke the JSON: %v", err)
	}
	for _, r := range doc.Results {
		if r.Explain == nil {
			t.Fatalf("%s carries no explain record", r.Repository)
		}
		if r.Repository == "charts-mixed" && r.Explain.ByType["CHART"] != 1 {
			t.Errorf("charts-mixed by_type = %v, want one CHART", r.Explain.ByType)
		}
	}

	// Without the flag the field stays out of the document entirely.
	bare, _, _ := runCLI(t, srv, "-o", "json", "platform")
	if strings.Contains(bare, "\"explain\"") {
		t.Error("explain should be omitted from JSON unless asked for")
	}
}

func TestFailOnThresholds(t *testing.T) {
	srv := fakeHarbor(t)
	tests := []struct {
		failOn string
		want   int
	}{
		{"fail", exitFailures}, // one repository 404s
		{"warn", exitFailures}, // warnings also present
		{"never", exitOK},
	}
	for _, tt := range tests {
		t.Run(tt.failOn, func(t *testing.T) {
			if _, _, code := runCLI(t, srv, "--fail-on", tt.failOn, "platform"); code != tt.want {
				t.Errorf("--fail-on %s: exit = %d, want %d", tt.failOn, code, tt.want)
			}
		})
	}
}

// --help and --version are the tools you reach for when everything else is
// broken, so they must not need configuration or credentials.
func TestHelpAndVersionNeedNoEnvironment(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"-h"}, {"--version"}, {"-V"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			t.Setenv("HARBOR_URL", "")
			t.Setenv("HARBOR_USER", "")
			t.Setenv("HARBOR_PASSWORD", "")

			var out, errBuf bytes.Buffer
			code := Run(args, strings.NewReader(""), &out, &errBuf)
			if code != exitOK {
				t.Errorf("exit = %d, want 0", code)
			}
			if out.Len() == 0 {
				t.Error("nothing written to stdout")
			}
			if errBuf.Len() != 0 {
				t.Errorf("unexpected stderr: %q", errBuf.String())
			}
		})
	}
}

func TestUsageErrors(t *testing.T) {
	srv := fakeHarbor(t)
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"missing scope", []string{}, "Missing argument"},
		{"unknown option", []string{"--nope", "platform"}, "unknown option"},
		{"bad output format", []string{"--output", "yaml", "platform"}, "invalid --output"},
		{"bad concurrency", []string{"--concurrency", "0", "platform"}, "invalid --concurrency"},
		{"non-numeric concurrency", []string{"--concurrency", "lots", "platform"}, "expected an integer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, stderr, code := runCLI(t, srv, tt.args...)
			if code != exitUsage {
				t.Errorf("exit = %d, want %d", code, exitUsage)
			}
			if !strings.Contains(stderr, tt.want) {
				t.Errorf("stderr = %q, want it to mention %q", stderr, tt.want)
			}
		})
	}
}

func TestMissingRegistryURL(t *testing.T) {
	t.Setenv("OCI_ARTIFACT_STAT_URL", "")
	t.Setenv("HARBOR_URL", "")
	t.Setenv("NO_COLOR", "1")
	var out, errBuf bytes.Buffer
	if code := Run([]string{"platform"}, strings.NewReader(""), &out, &errBuf); code != exitUsage {
		t.Errorf("exit = %d, want %d", code, exitUsage)
	}
	if !strings.Contains(errBuf.String(), "OCI_ARTIFACT_STAT_URL") {
		t.Errorf("stderr = %q, want it to name the missing variable", errBuf.String())
	}
}

func TestDeprecatedCurlEnvIsHonoredAndAnnounced(t *testing.T) {
	t.Setenv("CURL_RETRY", "3")
	_, stderr, _ := runCLI(t, fakeHarbor(t), "platform")
	if !strings.Contains(stderr, "CURL_RETRY is deprecated") {
		t.Errorf("stderr = %q, want a deprecation notice", stderr)
	}
}

func compareGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("writing golden: %v", err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading golden (run: go test ./internal/cli -update): %v", err)
	}
	if got != string(want) {
		t.Errorf("output differs from %s:\n%s", name, diffLines(string(want), got))
	}
}

func diffLines(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	var b strings.Builder
	for i := 0; i < max(len(w), len(g)); i++ {
		var lw, lg string
		if i < len(w) {
			lw = w[i]
		}
		if i < len(g) {
			lg = g[i]
		}
		if lw != lg {
			fmt.Fprintf(&b, "  line %d:\n    want: %q\n    got:  %q\n", i+1, lw, lg)
		}
	}
	return b.String()
}
