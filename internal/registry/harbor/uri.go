package harbor

import (
	"io"
	"net/http"
	"strings"
)

// maxBodyBytes caps how much of a response is read, so a malformed or
// hostile endpoint cannot exhaust memory.
const maxBodyBytes = 64 << 20

func readAllLimited(resp *http.Response) ([]byte, error) {
	return io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
}

const upperhex = "0123456789ABCDEF"

// escapeURI percent-encodes everything outside RFC 3986's unreserved set.
// Neither url.PathEscape nor url.QueryEscape will do: repository names
// contain slashes that must arrive as %2F.
func escapeURI(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(upperhex[c>>4])
			b.WriteByte(upperhex[c&0x0f])
		}
	}
	return b.String()
}
