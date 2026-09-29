package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/corn-xi/oci-artifact-stat/internal/audit"
)

func showHelp(w io.Writer, prog string) {
	fmt.Fprintf(w, `Usage: %s [OPTIONS] <scope|reference...>

Reports the actual highest-published version of everything in a registry
scope, flagging stale or duplicate publishing along the way.

Speaks Harbor's REST API and the OCI Distribution Spec, detecting which by
probing the registry.

Arguments:
  <scope>                A namespace to audit, when %s
                          names the registry: a Harbor project by name or
                          numeric ID, or an OCI namespace. Not required with
                          --list-scopes.
  <reference...>         One or more full references instead, each carrying
                          its own host: ghcr.io/org/repo. This is the only
                          form that works on registries which do not
                          implement /v2/_catalog, Docker Hub and GHCR among
                          them. All references must be on one registry.

Options:
  --list-scopes          List every scope visible to your credentials (ID,
                          name, repo count, visibility) and exit -- use this
                          when you don't know what to pass.
  --raw-tags             Show the exact tag as pushed (e.g.
                          "2.18.0-rel-build42") instead of the parsed
                          version. Useful when you need the literal tag, for
                          example to pull it.
  --artifact-type TYPE   Which artifact type to audit: auto (default),
                          image, chart, or whatever else the registry
                          publishes. Under auto the run reads images and
                          switches if another type's tags parse markedly
                          better, saying so when it does. One type per run,
                          never mixed -- a chart and an image sharing a
                          repository path are different things, and comparing
                          their versions is meaningless.
  --include-prereleases  Let prereleases (rc, alpha, beta, snapshot, ...) win
                          the version selection. By default they are excluded,
                          since "the latest version" normally means the latest
                          released one; a repository that has published
                          nothing else still reports its prerelease.
  -o, --output FORMAT    table (default), json or csv. With json or csv,
                          stdout carries only the document; all progress and
                          diagnostics go to stderr.
  --fail-on LEVEL        Which verdict makes the command exit non-zero:
                          fail (default), warn, or never.
  --ignore RULE[,RULE]   Suppress findings. Repeatable. Each RULE is a bare
                          CODE, or REPOSITORY:CODE to silence it in one
                          repository only -- prefer the scoped form, so a
                          finding that is expected in one place does not go
                          unnoticed everywhere else. A suppressed finding
                          affects neither the report nor the exit status.
                          Codes:
%s
  --explain              Show what the analysis actually saw per repository:
                          artifact counts by type, how many tags parsed as
                          versions, what was discarded. Use it to check that
                          this tool reads your registry the way you expect --
                          and to paste into a bug report when it does not.
  --concurrency N        Repositories to inspect in parallel (default: %d).
  --artifact-window N    How many recent artifacts to inspect per repository
                          when picking a version (default: %d).
  --password-stdin       Read the secret from stdin instead of prompting: a
                          password when a username is known, otherwise a
                          token.
  --http SPEC            Network bounds, as a preset or key=value pairs,
                          comma-separated and applied in order:
                            --http patient
                            --http timeout=30s,retries=4
                            --http patient,retries=5
                          Presets: fast, default, patient. Keys:
                          connect-timeout, timeout, retries, retry-delay.
                          Durations take a bare number of seconds or a Go
                          duration such as "1m30s". Retries apply to
                          transient failures only -- no response, 429 or 5xx;
                          other 4xx are never retried.
  -h, --help             Show this help and exit.
  -V, --version          Print the version and exit.

Credentials are never hardcoded and the secret never appears in argv. Sources
are tried in order, first hit wins:

  1. %s, or
     %s and %s
  2. --password-stdin
  3. ~/.docker/config.json, including credential helpers -- so a registry you
     have already run "docker login" against needs nothing here
  4. an interactive prompt, only when stdin is a terminal
  5. anonymous, which is how public registries and public Harbor projects are
     read

Environment:
  %s
                         Registry base URL, e.g. https://harbor.example.com.
                         Not needed when every argument is a full reference.
  %s
                         Bearer or identity token.
  %s
                         Username, to skip the prompt.
  %s
                         Password, to skip the prompt.
  DOCKER_CONFIG          Directory holding config.json, if not ~/.docker.
  NO_COLOR               Disable ANSI-colored status tags.
  OCI_IMAGE_STAT_*, HARBOR_*
                         Deprecated aliases for the four above, still honored.
  CURL_CONNECT_TIMEOUT, CURL_MAX_TIME, CURL_RETRY, CURL_RETRY_DELAY
                         Deprecated aliases for --http.

Exit status:
  0   Completed; nothing met the --fail-on threshold.
  1   The threshold was met, or a top-level API call failed.
  2   The command was invoked incorrectly.
`, prog, envURL,
		wrapCodes(audit.AllCodes, 26, 78),
		defaultConcurrency, defaultArtifactWindow,
		envToken, envUser, envPassword,
		envURL, envToken, envUser, envPassword)
}

// wrapCodes lays the finding codes out under the flag description without
// running past the help text's width.
func wrapCodes(codes []string, indent, width int) string {
	pad := strings.Repeat(" ", indent)
	var lines []string
	line := pad
	for i, code := range codes {
		piece := code
		if i < len(codes)-1 {
			piece += ","
		}
		if len(line) > indent && len(line)+1+len(piece) > width {
			lines = append(lines, line)
			line = pad
		}
		if len(line) > indent {
			line += " "
		}
		line += piece
	}
	return strings.Join(append(lines, line), "\n")
}
