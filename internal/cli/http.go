package cli

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// httpPreset is a named set of network bounds. Separate flags for the four
// values were four decisions almost nobody wanted to make; a preset covers
// the cases that differ and key=value overrides keep full control.
type httpPreset struct {
	connectTimeout time.Duration
	maxTime        time.Duration
	retries        int
	retryDelay     time.Duration
}

var httpPresets = map[string]httpPreset{
	"fast":    {2 * time.Second, 5 * time.Second, 0, 500 * time.Millisecond},
	"default": {defaultConnectTimeout, defaultMaxTime, defaultRetries, defaultRetryDelay},
	"patient": {10 * time.Second, 60 * time.Second, 3, 5 * time.Second},
}

// httpKeys are the individual settings, for validation and for the error
// message that lists them.
var httpKeys = []string{"connect-timeout", "timeout", "retries", "retry-delay"}

// applyHTTP parses a --http value: a comma-separated list where a bare word
// is a preset and anything with "=" overrides one setting. Later entries win,
// so "patient,retries=5" reads as "the patient preset, but five retries".
func (c *config) applyHTTP(spec string) error {
	for _, entry := range strings.Split(spec, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		key, raw, isOverride := strings.Cut(entry, "=")
		if !isOverride {
			preset, ok := httpPresets[entry]
			if !ok {
				return usagef("invalid --http %q: want a preset (%s) or key=value (%s)",
					entry, strings.Join(presetNames(), ", "), strings.Join(httpKeys, ", "))
			}
			c.connectTimeout = preset.connectTimeout
			c.maxTime = preset.maxTime
			c.retries = preset.retries
			c.retryDelay = preset.retryDelay
			continue
		}

		// A typo in a key must fail loudly; silently ignoring it would leave
		// the run using bounds the caller did not ask for.
		switch strings.TrimSpace(key) {
		case "connect-timeout":
			d, err := durationValue(raw, nil)
			if err != nil {
				return err
			}
			c.connectTimeout = d
		case "timeout":
			d, err := durationValue(raw, nil)
			if err != nil {
				return err
			}
			c.maxTime = d
		case "retry-delay":
			d, err := durationValue(raw, nil)
			if err != nil {
				return err
			}
			c.retryDelay = d
		case "retries":
			n, err := strconv.Atoi(strings.TrimSpace(raw))
			if err != nil || n < 0 {
				return usagef("invalid --http retries=%q: want zero or more", raw)
			}
			c.retries = n
		default:
			return usagef("invalid --http key %q: want one of %s", key, strings.Join(httpKeys, ", "))
		}
	}
	return nil
}

func presetNames() []string {
	names := make([]string, 0, len(httpPresets))
	for name := range httpPresets {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
