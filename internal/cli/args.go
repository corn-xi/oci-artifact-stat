package cli

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/corn-xi/oci-artifact-stat/internal/audit"
)

// usageError marks a problem with how the command was invoked, which exits
// with a distinct status so scripts can tell "you called this wrong" apart
// from "the audit found failures".
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error {
	return &usageError{fmt.Sprintf(format, args...)}
}

// parseArgs fills cfg from the command line.
//
// The loop accepts flags before or after the positional argument, matching
// convention: "oci-artifact-stat myproject --raw-tags" reads naturally, and
// stdlib flag parsing would quietly stop at the positional instead.
func parseArgs(args []string, cfg *config) (help, version bool, err error) {
	i := 0
	// value returns the current flag's argument, from either --flag=v or the
	// following word.
	value := func(name, inline string, hasInline bool) (string, error) {
		if hasInline {
			return inline, nil
		}
		if i+1 >= len(args) {
			return "", usagef("option %s requires a value", name)
		}
		i++
		return args[i], nil
	}

	for ; i < len(args); i++ {
		name, inline, hasInline := strings.Cut(args[i], "=")

		switch name {
		case "-h", "--help":
			return true, false, nil
		case "-V", "--version":
			return false, true, nil

		case "--raw-tags":
			cfg.rawTags = true
		case "--list-scopes", "--list-projects": // older name still accepted
			cfg.listScopes = true
		case "--password-stdin":
			cfg.passwordStdin = true
		case "--include-prereleases":
			cfg.includePrereleases = true
		case "--explain":
			cfg.explain = true

		case "--ignore":
			v, err := value(name, inline, hasInline)
			if err != nil {
				return false, false, err
			}
			// Repeatable, comma-separated within one use, and each entry is
			// either a bare code or "repository:code" to scope it.
			for _, entry := range strings.Split(v, ",") {
				if entry = strings.TrimSpace(entry); entry == "" {
					continue
				}
				rule := audit.IgnoreRule{Code: entry}
				if repo, code, scoped := strings.Cut(entry, ":"); scoped {
					rule = audit.IgnoreRule{Repository: repo, Code: code}
				}
				cfg.ignore = append(cfg.ignore, rule)
			}

		case "--http":
			v, err := value(name, inline, hasInline)
			if err != nil {
				return false, false, err
			}
			if err := cfg.applyHTTP(v); err != nil {
				return false, false, err
			}

		case "--artifact-type":
			if cfg.artifactType, err = stringValue(value(name, inline, hasInline)); err != nil {
				return false, false, err
			}

		case "-o", "--output":
			v, err := value(name, inline, hasInline)
			if err != nil {
				return false, false, err
			}
			cfg.output = outputFormat(v)

		case "--fail-on":
			v, err := value(name, inline, hasInline)
			if err != nil {
				return false, false, err
			}
			cfg.failOn = failOn(v)

		case "--concurrency":
			if cfg.concurrency, err = intValue(value(name, inline, hasInline)); err != nil {
				return false, false, err
			}
		case "--artifact-window":
			if cfg.artifactWindow, err = intValue(value(name, inline, hasInline)); err != nil {
				return false, false, err
			}
		case "--retries":
			if cfg.retries, err = intValue(value(name, inline, hasInline)); err != nil {
				return false, false, err
			}

		default:
			if strings.HasPrefix(name, "-") && name != "-" {
				return false, false, usagef("unknown option: %s", args[i])
			}
			cfg.args = append(cfg.args, args[i])
		}
	}
	return false, false, nil
}

func stringValue(raw string, err error) (string, error) { return raw, err }

func intValue(raw string, err error) (int, error) {
	if err != nil {
		return 0, err
	}
	n, convErr := strconv.Atoi(raw)
	if convErr != nil {
		return 0, usagef("expected an integer, got %q", raw)
	}
	return n, nil
}

// durationValue accepts a Go duration ("15s", "2m") or a bare number of
// seconds, since the environment variables it replaces were plain seconds.
func durationValue(raw string, err error) (time.Duration, error) {
	if err != nil {
		return 0, err
	}
	if secs, convErr := strconv.ParseFloat(raw, 64); convErr == nil {
		if secs < 0 {
			return 0, usagef("expected a non-negative duration, got %q", raw)
		}
		return time.Duration(secs * float64(time.Second)), nil
	}
	d, convErr := time.ParseDuration(raw)
	if convErr != nil || d < 0 {
		return 0, usagef("expected a duration such as 15s, got %q", raw)
	}
	return d, nil
}
