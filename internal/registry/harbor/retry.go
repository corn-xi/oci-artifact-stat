package harbor

import (
	"math/rand"
	"net/http"
	"strconv"
	"time"
)

// RetryPolicy bounds how hard a single request is retried.
//
// Retries is a count of RETRIES, not attempts: a request is tried once and
// then retried up to Retries more times.
type RetryPolicy struct {
	Retries int
	Delay   time.Duration
}

// retryable reports whether an outcome is worth trying again: a transport
// failure, a 5xx, or a 429. Every other 4xx is final, since no amount of
// retrying fixes a bad request, missing credentials or a 404.
func retryable(code int) bool {
	return code == 0 || code == http.StatusTooManyRequests || code >= 500
}

// backoff returns how long to wait before the given retry (1-based).
// Exponential with full jitter, since concurrent workers hit the same rate
// limit at the same instant and a fixed delay marches them back in lockstep.
// A server's Retry-After always wins.
func (p RetryPolicy) backoff(attempt int, header http.Header) time.Duration {
	if header != nil {
		if d, ok := parseRetryAfter(header.Get("Retry-After")); ok {
			return d
		}
	}
	d := p.Delay << (attempt - 1)
	if d <= 0 {
		return 0
	}
	return d/2 + time.Duration(rand.Int63n(int64(d/2)+1))
}

// parseRetryAfter handles both forms the header takes: delay-seconds and an
// HTTP date.
func parseRetryAfter(v string) (time.Duration, bool) {
	if v == "" {
		return 0, false
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0, false
		}
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d, true
		}
		return 0, true
	}
	return 0, false
}
