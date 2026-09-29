package harbor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/corn-xi/oci-artifact-stat/internal/auth"
	"github.com/corn-xi/oci-artifact-stat/internal/registry"
	"github.com/corn-xi/oci-artifact-stat/internal/ui"
)

// testClient builds a client pointed at srv with retries fast enough not to
// slow the suite down.
func testClient(t *testing.T, srv *httptest.Server, retries int) *Client {
	t.Helper()
	return New(srv.URL, auth.Credentials{Username: "user", Password: "pass"}, Options{
		ConnectTimeout: time.Second,
		MaxTime:        5 * time.Second,
		Retry:          RetryPolicy{Retries: retries, Delay: time.Millisecond},
		Logger:         ui.Logger{Out: io.Discard, Err: io.Discard},
	})
}

func TestListRepositoriesPaginatesUsingTotalCount(t *testing.T) {
	const total = 250
	var pagesServed atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pagesServed.Add(1)
		page := r.URL.Query().Get("page")
		var items []map[string]string
		start := 0
		switch page {
		case "1":
			start = 0
		case "2":
			start = 100
		case "3":
			start = 200
		default:
			t.Errorf("unexpected page %q", page)
		}
		for i := start; i < min(start+100, total); i++ {
			items = append(items, map[string]string{"name": fmt.Sprintf("proj/repo-%03d", i)})
		}
		w.Header().Set("X-Total-Count", fmt.Sprint(total))
		json.NewEncoder(w).Encode(items)
	}))
	defer srv.Close()

	repos, err := testClient(t, srv, 0).ListRepositories(context.Background(), registry.Scope{Name: "proj"})
	if err != nil {
		t.Fatalf("ListRepositories: %v", err)
	}
	if len(repos) != total {
		t.Errorf("got %d repositories, want %d -- pagination dropped rows", len(repos), total)
	}
	if got := pagesServed.Load(); got != 3 {
		t.Errorf("served %d pages, want 3", got)
	}
}

// Some deployments omit or miscount X-Total-Count, so a short page has to end
// the walk on its own.
func TestListRepositoriesStopsOnShortPageWithoutTotalCount(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var items []map[string]string
		if r.URL.Query().Get("page") == "1" {
			for i := 0; i < 42; i++ {
				items = append(items, map[string]string{"name": fmt.Sprintf("proj/repo-%d", i)})
			}
		} else {
			t.Errorf("walked past the short page")
		}
		json.NewEncoder(w).Encode(items)
	}))
	defer srv.Close()

	repos, err := testClient(t, srv, 0).ListRepositories(context.Background(), registry.Scope{Name: "proj"})
	if err != nil {
		t.Fatalf("ListRepositories: %v", err)
	}
	if len(repos) != 42 {
		t.Errorf("got %d repositories, want 42", len(repos))
	}
}

// The /repositories endpoint is not reliably scoped by project_name, so the
// client-side prefix filter is load-bearing rather than belt-and-braces.
func TestListRepositoriesDropsForeignProjects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]string{
			{"name": "proj/keep-me"},
			{"name": "other/intruder"},
			{"name": "projector/looks-similar"},
		})
	}))
	defer srv.Close()

	repos, err := testClient(t, srv, 0).ListRepositories(context.Background(), registry.Scope{Name: "proj"})
	if err != nil {
		t.Fatalf("ListRepositories: %v", err)
	}
	if len(repos) != 1 || repos[0].Name != "keep-me" {
		t.Fatalf("got %+v, want just the prefixed repository with its prefix stripped", repos)
	}
	if repos[0].FullName != "proj/keep-me" {
		t.Errorf("FullName = %q, want the unstripped name", repos[0].FullName)
	}
}

func TestListArtifactsEncodesSlashesInRepositoryName(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		w.Write([]byte(`[{"type":"IMAGE","push_time":"2026-09-01T10:00:00.000Z","tags":[{"name":"1.0.0"}]}]`))
	}))
	defer srv.Close()

	arts, err := testClient(t, srv, 0).ListArtifacts(context.Background(),
		registry.Scope{Name: "proj"}, registry.Repository{Name: "group/sub/repo"}, 20)
	if err != nil {
		t.Fatalf("ListArtifacts: %v", err)
	}
	if !strings.Contains(gotPath, "group%2Fsub%2Frepo") {
		t.Errorf("path = %q, want the repository name percent-encoded", gotPath)
	}
	if len(arts) != 1 || !arts[0].HasPushTime {
		t.Errorf("got %+v, want one artifact carrying a push time", arts)
	}
}

func TestRetriesTransientFailures(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		wantCalls   int32
		wantSuccess bool
	}{
		{"retries 5xx", http.StatusServiceUnavailable, 3, true},
		// 429 must be retried even though it is a 4xx: under concurrency it
		// is the ordinary answer to going too fast, not a refusal.
		{"retries 429", http.StatusTooManyRequests, 3, true},
		{"does not retry 404", http.StatusNotFound, 1, false},
		{"does not retry 401", http.StatusUnauthorized, 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Succeed on the third call, so a retrying status ends up OK
				// and a non-retrying one never gets there.
				if calls.Add(1) >= 3 {
					w.Write([]byte(`[]`))
					return
				}
				w.WriteHeader(tt.status)
			}))
			defer srv.Close()

			_, err := testClient(t, srv, 2).ListArtifacts(context.Background(),
				registry.Scope{Name: "p"}, registry.Repository{Name: "r"}, 20)

			if gotErr := err != nil; gotErr == tt.wantSuccess {
				t.Errorf("err = %v, wantSuccess = %v", err, tt.wantSuccess)
			}
			if got := calls.Load(); got != tt.wantCalls {
				t.Errorf("made %d requests, want %d", got, tt.wantCalls)
			}
			if !tt.wantSuccess {
				var apiErr *APIError
				if !asAPIError(err, &apiErr) || apiErr.Code != tt.status {
					t.Errorf("err = %v, want an APIError carrying HTTP %d", err, tt.status)
				}
			}
		})
	}
}

// An HTML body (an error page, a login wall, a CDN's catch-all index page)
// must not surface encoding/json's "invalid character '<'" as if the API had
// sent malformed JSON -- that reads as a tool bug, not a wrong URL.
func TestNonJSONBodyReportsAClearCause(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<!doctype html><html><body>not an API</body></html>`)
	}))
	defer srv.Close()

	_, err := testClient(t, srv, 0).ListScopes(context.Background())
	if err == nil {
		t.Fatal("ListScopes() = nil error, want one reporting a non-JSON body")
	}
	if strings.Contains(err.Error(), "invalid character") {
		t.Errorf("err = %q, leaked the raw json.Unmarshal message instead of explaining the cause", err)
	}
	if !strings.Contains(err.Error(), "not JSON") {
		t.Errorf("err = %q, want it to say the response was not JSON", err)
	}
}

func TestResolveScope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v2.0/projects/7":
			w.Write([]byte(`{"project_id":7,"name":"platform"}`))
		case r.URL.Path == "/api/v2.0/projects" && r.URL.Query().Get("name") == "platform":
			w.Write([]byte(`[{"project_id":7,"name":"platform"}]`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := testClient(t, srv, 0)

	byID, err := c.ResolveScope(context.Background(), "7")
	if err != nil || byID.Name != "platform" || byID.ID != 7 {
		t.Errorf("by id: got %+v, %v; want platform/7", byID, err)
	}
	byName, err := c.ResolveScope(context.Background(), "platform")
	if err != nil || byName.Name != "platform" || byName.ID != 7 {
		t.Errorf("by name: got %+v, %v; want platform/7", byName, err)
	}
	if _, err := c.ResolveScope(context.Background(), "nope"); err == nil {
		t.Error("resolving an unknown project should fail")
	}
}

// Anonymous runs send no Authorization header at all; public projects are
// readable that way, and demanding credentials for them made them
// unreachable.
func TestAuthorizationHeader(t *testing.T) {
	tests := []struct {
		name  string
		creds auth.Credentials
		want  string
	}{
		{"basic", auth.Credentials{Username: "u", Password: "p"}, "Basic dTpw"},
		{"bearer", auth.Credentials{Token: "abc123"}, "Bearer abc123"},
		{"token wins over a username", auth.Credentials{Username: "u", Token: "abc123"}, "Bearer abc123"},
		{"anonymous sends nothing", auth.Credentials{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			var seen bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got, seen = r.Header.Get("Authorization"), true
				w.Write([]byte(`[]`))
			}))
			defer srv.Close()

			c := New(srv.URL, tt.creds, Options{
				ConnectTimeout: time.Second, MaxTime: 5 * time.Second,
				Logger: ui.Logger{Out: io.Discard, Err: io.Discard},
			})
			if _, err := c.ListArtifacts(context.Background(),
				registry.Scope{Name: "p"}, registry.Repository{Name: "r"}, 20); err != nil {
				t.Fatalf("ListArtifacts: %v", err)
			}
			if !seen {
				t.Fatal("the server saw no request")
			}
			if got != tt.want {
				t.Errorf("Authorization = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseRetryAfter(t *testing.T) {
	if d, ok := parseRetryAfter("3"); !ok || d != 3*time.Second {
		t.Errorf("delay-seconds: got %v, %v", d, ok)
	}
	if d, ok := parseRetryAfter(time.Now().Add(2 * time.Second).UTC().Format(http.TimeFormat)); !ok || d <= 0 {
		t.Errorf("http-date: got %v, %v", d, ok)
	}
	if _, ok := parseRetryAfter("soon"); ok {
		t.Error("unparseable Retry-After should be ignored")
	}
	if _, ok := parseRetryAfter(""); ok {
		t.Error("absent Retry-After should be ignored")
	}
}

// asAPIError is errors.As, spelled out to keep the import list of the test
// file obvious.
func asAPIError(err error, target **APIError) bool {
	for err != nil {
		if e, ok := err.(*APIError); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
