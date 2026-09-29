// Package harbor implements the registry interfaces against Harbor's REST
// API v2.0. It uses plain net/http rather than the generated SDK: four
// endpoints are needed, and pagination and retries are where the behavior
// that matters lives.
package harbor

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/corn-xi/oci-artifact-stat/internal/auth"
	"github.com/corn-xi/oci-artifact-stat/internal/registry"
	"github.com/corn-xi/oci-artifact-stat/internal/ui"
)

// pageSize is used for every paginated list endpoint. Harbor's list
// endpoints cap silently, so an unpaginated call drops everything past the
// first page.
const pageSize = 100

// APIError reports a request that reached a conclusion other than 200.
// Code is 0 when no response was obtained at all.
type APIError struct {
	Code  int
	Label string
	// Err carries the transport failure behind a Code of 0, which would
	// otherwise be lost -- "no response" alone is a poor bug report.
	Err error
}

func (e *APIError) Error() string {
	if e.Code == 0 {
		if e.Err != nil {
			return fmt.Sprintf("%s: no response: %v", e.Label, e.Err)
		}
		return fmt.Sprintf("%s: no response", e.Label)
	}
	return fmt.Sprintf("%s: HTTP %d", e.Label, e.Code)
}

func (e *APIError) Unwrap() error { return e.Err }

// StatusCode returns the HTTP status, or 0 if the request never completed.
func (e *APIError) StatusCode() int { return e.Code }

// Options configures a Client.
type Options struct {
	ConnectTimeout time.Duration
	MaxTime        time.Duration
	Retry          RetryPolicy
	Logger         ui.Logger
}

// retryLogLimit is how many retries are announced as they happen. Enough to
// show the run is not hung, few enough not to bury the table that follows;
// the rest are counted and reported once.
const retryLogLimit = 3

// Client talks to one Harbor instance.
type Client struct {
	baseURL string
	// authHeader is the complete Authorization value, empty when the run is
	// anonymous -- public projects are readable that way.
	authHeader string
	http       *http.Client
	retry      RetryPolicy
	log        ui.Logger
	retries    atomic.Int64
}

// Retries reports how many requests were retried, for a closing summary.
func (c *Client) Retries() int { return int(c.retries.Load()) }

// New builds a client for baseURL. Basic covers both ordinary users and
// Harbor robot accounts; a token is sent as a bearer credential instead.
func New(baseURL string, creds auth.Credentials, opts Options) *Client {
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		authHeader: authorization(creds),
		http: &http.Client{
			// MaxTime bounds a single attempt end to end, so a hung backend
			// can never freeze the run; the retry loop above it bounds the
			// total.
			Timeout: opts.MaxTime,
			Transport: &http.Transport{
				DialContext:         (&net.Dialer{Timeout: opts.ConnectTimeout}).DialContext,
				TLSHandshakeTimeout: opts.ConnectTimeout,
				// The default of 2 throttles concurrent workers against a
				// single host, which is exactly the host we are talking to.
				MaxIdleConnsPerHost: 64,
			},
		},
		retry: opts.Retry,
		log:   opts.Logger,
	}
}

// authorization renders the credentials as an Authorization header value.
func authorization(creds auth.Credentials) string {
	switch {
	case creds.Token != "":
		return "Bearer " + creds.Token
	case creds.Username != "" || creds.Password != "":
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(creds.Username+":"+creds.Password))
	default:
		return ""
	}
}

// get performs one GET with retries, returning the body and headers. label
// describes the request for retry logs; a full URL is long enough to break
// the table layout it interleaves with.
func (c *Client) get(ctx context.Context, url, label string) ([]byte, http.Header, error) {
	for attempt := 0; ; attempt++ {
		body, header, code, err := c.attempt(ctx, url)
		if err != nil && ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		if code == http.StatusOK {
			return body, header, nil
		}
		if !retryable(code) || attempt >= c.retry.Retries {
			return nil, nil, &APIError{Code: code, Label: label, Err: err}
		}

		wait := c.retry.backoff(attempt+1, header)
		if c.retries.Add(1) <= retryLogLimit {
			c.log.Warn("%s failed (HTTP %s) -- retry %d/%d in %s...",
				label, codeString(code), attempt+1, c.retry.Retries, wait.Round(time.Millisecond))
		}

		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-time.After(wait):
		}
	}
}

// attempt performs exactly one request. A transport-level failure is reported
// as code 0 rather than an error, so the retry decision has a single shape.
func (c *Client) attempt(ctx context.Context, url string) ([]byte, http.Header, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, 0, err
	}
	if c.authHeader != "" {
		req.Header.Set("Authorization", c.authHeader)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, 0, err
	}
	defer resp.Body.Close()

	body, err := readAllLimited(resp)
	if err != nil {
		return nil, resp.Header, 0, err
	}
	return body, resp.Header, resp.StatusCode, nil
}

// codeString renders a status for the retry log, using "000" when there was
// no response at all.
func codeString(code int) string {
	if code == 0 {
		return "000"
	}
	return strconv.Itoa(code)
}

// unmarshalBody decodes a response body already confirmed to be HTTP 200,
// and when it does not even look like JSON -- an HTML error page, a login
// wall, a CDN's catch-all index page for an unmapped path -- says so instead
// of surfacing encoding/json's "invalid character '<'" as if the API itself
// had sent malformed JSON.
func unmarshalBody(body []byte, v any, label string) error {
	if err := json.Unmarshal(body, v); err != nil {
		if trimmed := strings.TrimSpace(string(body)); trimmed == "" || (trimmed[0] != '{' && trimmed[0] != '[') {
			return fmt.Errorf("%s: response was not JSON -- check the URL points at a registry API, not a web page", label)
		}
		return fmt.Errorf("%s: %w", label, err)
	}
	return nil
}

// fetchAllPages walks every page of a paginated endpoint. The separator is
// chosen from whether baseURL already carries a query string, so this serves
// both "/projects" and "/repositories?project_name=X".
func (c *Client) fetchAllPages(ctx context.Context, baseURL, label string) ([]json.RawMessage, error) {
	sep := "?"
	if strings.Contains(baseURL, "?") {
		sep = "&"
	}

	var all []json.RawMessage
	for page := 1; ; page++ {
		url := fmt.Sprintf("%s%spage=%d&page_size=%d", baseURL, sep, page, pageSize)
		body, header, err := c.get(ctx, url, fmt.Sprintf("%s (page %d)", label, page))
		if err != nil {
			return nil, err
		}

		var got []json.RawMessage
		if err := unmarshalBody(body, &got, fmt.Sprintf("%s page %d", label, page)); err != nil {
			return nil, err
		}
		all = append(all, got...)

		// Two independent stop conditions, both needed: X-Total-Count is
		// authoritative when present, and the short-page check covers
		// deployments that omit or miscount it.
		if total, err := strconv.Atoi(strings.TrimSpace(header.Get("X-Total-Count"))); err == nil {
			if total <= page*pageSize {
				break
			}
		}
		if len(got) < pageSize {
			break
		}
	}
	return all, nil
}

// Capabilities: Harbor returns push times with the artifact list, at no extra
// cost, so every check is available.
func (c *Client) Capabilities() registry.Capabilities {
	return registry.Capabilities{PushTimes: true, Windowed: true}
}

// --- Catalog ---

type apiProject struct {
	ProjectID int    `json:"project_id"`
	Name      string `json:"name"`
	RepoCount int    `json:"repo_count"`
	Metadata  struct {
		Public string `json:"public"`
	} `json:"metadata"`
}

func (p apiProject) toScope() registry.Scope {
	return registry.Scope{
		ID:        p.ProjectID,
		Name:      p.Name,
		RepoCount: p.RepoCount,
		Public:    p.Metadata.Public == "true",
	}
}

// ListScopes returns every project visible to the credentials. For a
// non-admin caller Harbor already scopes this server-side to projects the
// user holds a role in, so no client-side filtering is needed.
func (c *Client) ListScopes(ctx context.Context) ([]registry.Scope, error) {
	raw, err := c.fetchAllPages(ctx, c.baseURL+"/api/v2.0/projects", "project list")
	if err != nil {
		return nil, err
	}
	scopes := make([]registry.Scope, 0, len(raw))
	for _, item := range raw {
		var p apiProject
		if err := json.Unmarshal(item, &p); err != nil {
			return nil, fmt.Errorf("project list: %w", err)
		}
		scopes = append(scopes, p.toScope())
	}
	return scopes, nil
}

// ResolveScope accepts either a numeric project ID or a project name and
// fills in whichever half was not given. Both are needed downstream: listing
// repositories takes the name, while the ID is what the user sees in the URL.
func (c *Client) ResolveScope(ctx context.Context, ref string) (registry.Scope, error) {
	label := fmt.Sprintf("project '%s'", ref)

	if id, err := strconv.Atoi(ref); err == nil && !strings.HasPrefix(ref, "-") {
		body, _, err := c.get(ctx, fmt.Sprintf("%s/api/v2.0/projects/%d", c.baseURL, id), label)
		if err != nil {
			return registry.Scope{}, err
		}
		var p apiProject
		if err := unmarshalBody(body, &p, label); err != nil {
			return registry.Scope{}, err
		}
		if p.Name == "" {
			return registry.Scope{}, errNotResolved(ref)
		}
		return registry.Scope{ID: id, Name: p.Name}, nil
	}

	// Harbor's name= filter is a fuzzy substring match, so the first hit may
	// not be exact. Its ID is only ever shown in the banner; every data query
	// uses the name the user supplied.
	body, _, err := c.get(ctx, c.baseURL+"/api/v2.0/projects?name="+escapeURI(ref), label)
	if err != nil {
		return registry.Scope{}, err
	}
	var found []apiProject
	if err := unmarshalBody(body, &found, label); err != nil {
		return registry.Scope{}, err
	}
	if len(found) == 0 || found[0].ProjectID == 0 {
		return registry.Scope{}, errNotResolved(ref)
	}
	return registry.Scope{ID: found[0].ProjectID, Name: ref}, nil
}

func errNotResolved(ref string) error {
	return fmt.Errorf("could not find project '%s' or resolve its ID/name", ref)
}

// ListRepositories returns the repositories belonging to a scope. The
// client-side prefix filter is load-bearing: /repositories is not reliably
// scoped by project_name alone.
func (c *Client) ListRepositories(ctx context.Context, scope registry.Scope) ([]registry.Repository, error) {
	raw, err := c.fetchAllPages(ctx,
		c.baseURL+"/api/v2.0/repositories?project_name="+escapeURI(scope.Name),
		"repository list")
	if err != nil {
		return nil, err
	}

	prefix := scope.Name + "/"
	repos := make([]registry.Repository, 0, len(raw))
	for _, item := range raw {
		var r struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(item, &r); err != nil {
			return nil, fmt.Errorf("repository list: %w", err)
		}
		if !strings.HasPrefix(r.Name, prefix) {
			continue
		}
		repos = append(repos, registry.Repository{
			Name:     strings.TrimPrefix(r.Name, prefix),
			FullName: r.Name,
		})
	}
	return repos, nil
}

// --- Inspector ---

type apiArtifact struct {
	Type     string `json:"type"`
	PushTime string `json:"push_time"`
	Tags     []struct {
		Name string `json:"name"`
	} `json:"tags"`
}

// ListArtifacts returns up to limit of the most recently pushed artifacts,
// newest first, with their tags. Harbor answers in one request per
// repository, push times included.
func (c *Client) ListArtifacts(ctx context.Context, scope registry.Scope, repo registry.Repository, limit int) ([]registry.Artifact, error) {
	url := fmt.Sprintf("%s/api/v2.0/projects/%s/repositories/%s/artifacts?page_size=%d&with_tag=true&sort=-push_time",
		c.baseURL, escapeURI(scope.Name), escapeURI(repo.Name), limit)

	body, _, err := c.get(ctx, url, fmt.Sprintf("'%s' artifacts", repo.Name))
	if err != nil {
		return nil, err
	}

	var raw []apiArtifact
	if err := unmarshalBody(body, &raw, fmt.Sprintf("'%s' artifacts", repo.Name)); err != nil {
		return nil, err
	}

	artifacts := make([]registry.Artifact, 0, len(raw))
	for _, a := range raw {
		art := registry.Artifact{Type: a.Type}
		for _, t := range a.Tags {
			art.Tags = append(art.Tags, t.Name)
		}
		if ts, err := time.Parse(time.RFC3339, a.PushTime); err == nil {
			art.PushTime, art.HasPushTime = ts, true
		}
		artifacts = append(artifacts, art)
	}
	return artifacts, nil
}

// Probe reports whether a base URL is a Harbor instance.
//
// /api/v2.0/systeminfo answers without credentials, which is what makes
// backend detection possible without asking the user to declare it. A 200
// alone is not proof: an S3/CloudFront-backed single-page app answers 200
// with its own index page for any path, systeminfo included. The body must
// at least be a JSON object -- not a specific field, since an anonymous
// caller does not always get harbor_version back.
func Probe(ctx context.Context, baseURL string, opts Options) bool {
	c := New(baseURL, auth.Credentials{}, opts)
	body, _, err := c.get(ctx, c.baseURL+"/api/v2.0/systeminfo", "registry probe")
	if err != nil {
		return false
	}
	var obj map[string]json.RawMessage
	return json.Unmarshal(body, &obj) == nil
}
