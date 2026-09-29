package audit

import (
	"context"
	"errors"
	"net"
)

// TimedOut counts repositories whose fetch ran out of time.
//
// Worth separating from other failures: a registry that is merely slow looks
// exactly like one that is broken, and the remedy is a flag rather than a
// bug report.
func (r Run) TimedOut() int {
	n := 0
	for _, res := range r.Results {
		if isTimeout(res.Err) {
			n++
		}
	}
	return n
}

func isTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
