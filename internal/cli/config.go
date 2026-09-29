package cli

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/corn-xi/oci-artifact-stat/internal/audit"
)

// Version is the released version of this binary. A var, not a const, so
// releases can stamp the tag with -ldflags -X.
//
// Still 0.x: the CLI surface is settled, but the finding rules have been
// revised several times as new registries showed shapes the previous rule
// misread, and --ldflags exists in anticipation of more.
var Version = "0.1.0"

// Defaults bounding every request, so a hung backend cannot freeze a run.
const (
	defaultConnectTimeout = 5 * time.Second
	defaultMaxTime        = 15 * time.Second
	defaultRetries        = 1
	defaultRetryDelay     = 2 * time.Second
	defaultArtifactWindow = 20

	// Deliberately modest. Harbor rate-limits, and the point of a bounded
	// pool is to be faster than serial without becoming a source of 429s.
	defaultConcurrency = 8
)

// Environment variables are namespaced by the tool name, derived from the
// binary by the usual rule: uppercase, non-alphanumerics to underscores. That
// makes them guessable rather than memorized, the way GOLANGCI_LINT_* follows
// from golangci-lint. The older names are still honored, with one notice.
const (
	envURL      = "OCI_ARTIFACT_STAT_URL"
	envUser     = "OCI_ARTIFACT_STAT_USER"
	envPassword = "OCI_ARTIFACT_STAT_PASSWORD"
	envToken    = "OCI_ARTIFACT_STAT_TOKEN"
)

// Deprecated names, still honored. The tool was harbor-image-stat before it
// spoke anything but Harbor, and oci-image-stat before artifact types became
// selectable.
var (
	envLegacyURL      = []string{"OCI_IMAGE_STAT_URL", "HARBOR_URL"}
	envLegacyUser     = []string{"OCI_IMAGE_STAT_USER", "HARBOR_USER"}
	envLegacyPassword = []string{"OCI_IMAGE_STAT_PASSWORD", "HARBOR_PASSWORD"}
)

// lookupEnv reads the canonical variable, falling back to its deprecated
// aliases in order and saying so once.
func lookupEnv(canonical string, deprecated []string, warn func(string, ...any)) string {
	if v := os.Getenv(canonical); v != "" {
		return v
	}
	for _, name := range deprecated {
		if v := os.Getenv(name); v != "" {
			warn("%s is deprecated, use %s", name, canonical)
			return v
		}
	}
	return ""
}

type outputFormat string

const (
	outputTable outputFormat = "table"
	outputJSON  outputFormat = "json"
	outputCSV   outputFormat = "csv"
)

type failOn string

const (
	failOnFail  failOn = "fail"
	failOnWarn  failOn = "warn"
	failOnNever failOn = "never"
)

type config struct {
	registryURL string
	// args are the positional arguments: one scope, or one or more
	// repository references.
	args []string

	listScopes         bool
	rawTags            bool
	passwordStdin      bool
	includePrereleases bool
	explain            bool

	// ignore suppresses findings, globally or for one repository; a
	// suppressed finding affects neither the report nor the exit status.
	ignore audit.IgnoreSet

	output         outputFormat
	failOn         failOn
	concurrency    int
	artifactWindow int
	artifactType   string

	connectTimeout time.Duration
	maxTime        time.Duration
	retries        int
	retryDelay     time.Duration
}

func defaultConfig() config {
	return config{
		output:         outputTable,
		failOn:         failOnFail,
		concurrency:    defaultConcurrency,
		artifactWindow: defaultArtifactWindow,
		artifactType:   audit.TypeAuto,
		connectTimeout: defaultConnectTimeout,
		maxTime:        defaultMaxTime,
		retries:        defaultRetries,
		retryDelay:     defaultRetryDelay,
	}
}

// applyEnv reads the legacy CURL_* knobs, which automation already sets.
// Each is honored once and reported as deprecated on stderr.
func (c *config) applyEnv(warn func(string, ...any)) {
	type envDur struct {
		name   string
		target *time.Duration
		flag   string
	}
	for _, e := range []envDur{
		{"CURL_CONNECT_TIMEOUT", &c.connectTimeout, "--connect-timeout"},
		{"CURL_MAX_TIME", &c.maxTime, "--timeout"},
		{"CURL_RETRY_DELAY", &c.retryDelay, "--retry-delay"},
	} {
		v := os.Getenv(e.name)
		if v == "" {
			continue
		}
		secs, err := strconv.ParseFloat(v, 64)
		if err != nil || secs < 0 {
			warn("ignoring %s=%q: not a non-negative number of seconds", e.name, v)
			continue
		}
		*e.target = time.Duration(secs * float64(time.Second))
		warn("%s is deprecated, use %s", e.name, e.flag)
	}

	if v := os.Getenv("CURL_RETRY"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			warn("ignoring CURL_RETRY=%q: not a non-negative integer", v)
		} else {
			c.retries = n
			warn("CURL_RETRY is deprecated, use --retries")
		}
	}
}

func (c *config) validate() error {
	switch c.output {
	case outputTable, outputJSON, outputCSV:
	default:
		return fmt.Errorf("invalid --output %q: want table, json or csv", c.output)
	}
	switch c.failOn {
	case failOnFail, failOnWarn, failOnNever:
	default:
		return fmt.Errorf("invalid --fail-on %q: want fail, warn or never", c.failOn)
	}
	if c.concurrency < 1 {
		return fmt.Errorf("invalid --concurrency %d: want at least 1", c.concurrency)
	}
	if c.artifactWindow < 1 {
		return fmt.Errorf("invalid --artifact-window %d: want at least 1", c.artifactWindow)
	}
	// Not validated against a fixed list: registries invent artifact types,
	// and refusing an unknown one would only get in the way.
	if strings.TrimSpace(c.artifactType) == "" {
		return fmt.Errorf("invalid --artifact-type: want a type such as image or chart")
	}
	if c.retries < 0 {
		return fmt.Errorf("invalid --retries %d: want zero or more", c.retries)
	}
	// A typo in --ignore must not silently suppress nothing; that is the
	// failure mode where a CI gate looks green for the wrong reason.
	for _, rule := range c.ignore {
		if !slices.Contains(audit.AllCodes, rule.Code) {
			return fmt.Errorf("invalid --ignore code %q: want one of %s", rule.Code, strings.Join(audit.AllCodes, ", "))
		}
	}
	return nil
}

// shouldFail maps the summary onto the process exit status.
func (c *config) shouldFail(fails, warns int) bool {
	switch c.failOn {
	case failOnNever:
		return false
	case failOnWarn:
		return fails > 0 || warns > 0
	default:
		return fails > 0
	}
}
