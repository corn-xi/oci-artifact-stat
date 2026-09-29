package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/corn-xi/oci-artifact-stat/internal/audit"
)

// The README and --help describe one interface, and the two drift apart the
// moment a flag is added to only one of them. This checks they agree rather
// than trusting that someone remembered.

func helpText(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	showHelp(&buf, "oci-artifact-stat")
	return buf.String()
}

func readme(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatalf("reading README: %v", err)
	}
	// One is markdown and the other plain text; the backticks are formatting,
	// not content.
	return strings.ReplaceAll(string(body), "`", "")
}

var flagPattern = regexp.MustCompile(`--[a-z][a-z-]*`)

func flagSet(text string) map[string]bool {
	out := map[string]bool{}
	for _, f := range flagPattern.FindAllString(text, -1) {
		out[f] = true
	}
	return out
}

func sorted(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// optionsTable is the README's own list of flags, its Options section alone.
// Prose and examples are excluded on purpose: they invoke other commands, and
// "shasum --ignore-missing" is not a flag of this tool.
func optionsTable(t *testing.T, doc string) string {
	t.Helper()
	const heading = "\n## Options\n"
	start := strings.Index(doc, heading)
	if start < 0 {
		t.Fatal("the README has no Options section")
	}
	rest := doc[start+len(heading):]
	if end := strings.Index(rest, "\n## "); end >= 0 {
		return rest[:end]
	}
	return rest
}

func TestReadmeAndHelpDocumentTheSameFlags(t *testing.T) {
	doc := readme(t)
	inHelp, inReadme := flagSet(helpText(t)), flagSet(optionsTable(t, doc))

	var missing, invented []string
	for f := range inHelp {
		if !inReadme[f] {
			missing = append(missing, f)
		}
	}
	for f := range inReadme {
		if !inHelp[f] {
			invented = append(invented, f)
		}
	}
	sort.Strings(missing)
	sort.Strings(invented)

	if len(missing) > 0 {
		t.Errorf("flags in --help but not in the README: %v", missing)
	}
	if len(invented) > 0 {
		t.Errorf("flags in the README that --help does not offer: %v", invented)
	}
	if len(inHelp) < 10 {
		t.Errorf("only %v extracted from --help; the pattern has probably stopped matching", sorted(inHelp))
	}
}

func TestReadmeAndHelpDocumentTheSameEnvironment(t *testing.T) {
	help, doc := helpText(t), readme(t)

	for _, name := range []string{
		envURL, envToken, envUser, envPassword,
		"DOCKER_CONFIG", "NO_COLOR",
		"OCI_IMAGE_STAT_", "HARBOR_", "CURL_MAX_TIME",
	} {
		if !strings.Contains(help, name) {
			t.Errorf("--help does not mention %s", name)
		}
		if !strings.Contains(doc, name) {
			t.Errorf("the README does not mention %s", name)
		}
	}
}

// Finding codes are a contract: --ignore takes them and --fail-on acts on
// them, so both documents have to list every one.
func TestReadmeAndHelpListEveryFindingCode(t *testing.T) {
	help, doc := helpText(t), readme(t)
	for _, code := range audit.AllCodes {
		if !strings.Contains(help, code) {
			t.Errorf("--help does not list %s", code)
		}
		if !strings.Contains(doc, code) {
			t.Errorf("the README does not list %s", code)
		}
	}
}

func TestReadmeAndHelpAgreeOnExitStatus(t *testing.T) {
	help, doc := helpText(t), readme(t)
	for _, phrase := range []string{"--fail-on threshold", "invoked incorrectly"} {
		if !strings.Contains(help, phrase) {
			t.Errorf("--help no longer explains %q", phrase)
		}
		if !strings.Contains(doc, phrase) {
			t.Errorf("the README no longer explains %q", phrase)
		}
	}
}

// Defaults quoted in prose are the ones that rot first.
func TestReadmeQuotesTheRealDefaults(t *testing.T) {
	doc := readme(t)
	for _, want := range []string{
		"default: 8",  // concurrency
		"default: 20", // artifact window
		"Go 1.25",     // module floor
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("the README no longer states %q", want)
		}
	}
}
