// Package kong is a thin HTTP client for the Kong Admin API. Entities are kept
// as generic maps so plugin configs and version-specific fields pass through
// untouched.
package kong

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gefferson/kong-flow/backend/internal/oauth"
)

// Entity is a Kong object as returned by the Admin API.
type Entity map[string]any

func (e Entity) Str(k string) string {
	if v, ok := e[k].(string); ok {
		return v
	}
	return ""
}

func (e Entity) ID() string   { return e.Str("id") }
func (e Entity) Name() string { return e.Str("name") }

// RefID returns the id of a nested foreign-key object, e.g. `service: {id: ...}`.
func (e Entity) RefID(field string) string {
	switch v := e[field].(type) {
	case map[string]any:
		if s, ok := v["id"].(string); ok {
			return s
		}
	case string:
		return v
	}
	return ""
}

// Kinds handled by the tool, in dependency-safe creation order.
var Kinds = []string{"services", "upstreams", "targets", "consumers", "routes", "plugins"}

type AuthType string

const (
	AuthNone   AuthType = "none"
	AuthKey    AuthType = "key"    // apikey header (or custom header name)
	AuthRBAC   AuthType = "rbac"   // Kong-Admin-Token (Enterprise)
	AuthBearer AuthType = "bearer" // Authorization: Bearer <token>
	AuthBasic  AuthType = "basic"  // Authorization: Basic <base64 already provided>
	AuthOAuth2 AuthType = "oauth2" // client-credentials token fetched on demand
)

type Config struct {
	AdminURL      string
	AuthType      AuthType
	Secret        string
	AuthHeader    string // optional override of the header name for AuthKey
	Workspace     string // Kong Enterprise workspace
	TLSSkipVerify bool
	Timeout       time.Duration
	// Tokens supplies access tokens when AuthType is AuthOAuth2. It is shared
	// per connection so the token is minted once and reused until it expires.
	Tokens *oauth.TokenSource
}

type Client struct {
	cfg  Config
	base string
	http *http.Client
}

func New(cfg Config) *Client {
	if cfg.Timeout == 0 {
		cfg.Timeout = 20 * time.Second
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if cfg.TLSSkipVerify {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	base := strings.TrimRight(cfg.AdminURL, "/")
	if ws := strings.Trim(cfg.Workspace, "/"); ws != "" {
		base += "/" + ws
	}
	return &Client{cfg: cfg, base: base, http: &http.Client{Timeout: cfg.Timeout, Transport: tr}}
}

// APIError carries the Admin API status and body so the UI can show Kong's own
// validation messages.
type APIError struct {
	Status int
	Method string
	Path   string
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("kong %s %s: %d %s", e.Method, e.Path, e.Status, strings.TrimSpace(e.Body))
}

// do performs an Admin API call. With OAuth2 it also handles the case where
// Kong rejects a token the source still considered fresh (clock skew, a token
// revoked early): a replacement is minted and the call retried once.
func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var raw []byte
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		raw = b
	}

	if c.cfg.AuthType != AuthOAuth2 {
		return c.doOnce(ctx, method, path, raw, out, "")
	}
	if c.cfg.Tokens == nil {
		return fmt.Errorf("kong: OAuth2 is configured but no token source was provided")
	}

	tok, err := c.cfg.Tokens.Token(ctx)
	if err != nil {
		return err
	}
	err = c.doOnce(ctx, method, path, raw, out, tok.Header())
	if !isAuthRejection(err) {
		return err
	}
	fresh, refreshErr := c.cfg.Tokens.Refresh(ctx, tok.AccessToken)
	if refreshErr != nil {
		return refreshErr
	}
	if fresh.AccessToken == tok.AccessToken {
		return err // nothing new to try with
	}
	return c.doOnce(ctx, method, path, raw, out, fresh.Header())
}

func isAuthRejection(err error) bool {
	var apiErr *APIError
	if !asAPIError(err, &apiErr) {
		return false
	}
	return apiErr.Status == http.StatusUnauthorized || apiErr.Status == http.StatusForbidden
}

// doOnce issues a single request. `authorization`, when set, is used verbatim;
// otherwise the connection's static credential scheme applies.
func (c *Client) doOnce(ctx context.Context, method, path string, body []byte, out any, authorization string) error {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	u := path
	if !strings.HasPrefix(path, "http") {
		u = c.base + path
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	} else {
		c.applyStaticAuth(req)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode >= 400 {
		return &APIError{Status: resp.StatusCode, Method: method, Path: path, Body: string(respBody)}
	}
	if out != nil && len(respBody) > 0 {
		return json.Unmarshal(respBody, out)
	}
	return nil
}

func (c *Client) applyStaticAuth(req *http.Request) {
	switch c.cfg.AuthType {
	case AuthKey:
		h := c.cfg.AuthHeader
		if h == "" {
			h = "apikey"
		}
		req.Header.Set(h, c.cfg.Secret)
	case AuthRBAC:
		req.Header.Set("Kong-Admin-Token", c.cfg.Secret)
	case AuthBearer:
		req.Header.Set("Authorization", "Bearer "+c.cfg.Secret)
	case AuthBasic:
		req.Header.Set("Authorization", "Basic "+c.cfg.Secret)
	}
}

// Info reports node information: version, edition and available plugins.
type Info struct {
	Version  string   `json:"version"`
	Edition  string   `json:"edition"`
	Hostname string   `json:"hostname"`
	Plugins  []string `json:"plugins"`
}

func (c *Client) Info(ctx context.Context) (Info, error) {
	var raw struct {
		Version  string `json:"version"`
		Hostname string `json:"hostname"`
		Edition  string `json:"edition"`
		Plugins  struct {
			AvailableOnServer map[string]any `json:"available_on_server"`
		} `json:"plugins"`
		Configuration struct {
			Edition string `json:"edition"`
		} `json:"configuration"`
	}
	if err := c.do(ctx, http.MethodGet, "/", nil, &raw); err != nil {
		return Info{}, err
	}
	info := Info{Version: raw.Version, Hostname: raw.Hostname, Edition: raw.Edition}
	if info.Edition == "" {
		info.Edition = raw.Configuration.Edition
	}
	if info.Edition == "" {
		info.Edition = "community"
	}
	for name := range raw.Plugins.AvailableOnServer {
		info.Plugins = append(info.Plugins, name)
	}
	return info, nil
}

type listPage struct {
	Data   []Entity `json:"data"`
	Next   string   `json:"next"`
	Offset string   `json:"offset"`
}

// list walks all pages of a collection endpoint.
func (c *Client) list(ctx context.Context, path string) ([]Entity, error) {
	out := []Entity{}
	next := path
	for i := 0; next != "" && i < 200; i++ {
		var page listPage
		if err := c.do(ctx, http.MethodGet, next, nil, &page); err != nil {
			return nil, err
		}
		out = append(out, page.Data...)
		switch {
		case page.Next != "" && page.Next != next:
			next = page.Next
		case page.Offset != "":
			sep := "?"
			if strings.Contains(path, "?") {
				sep = "&"
			}
			next = path + sep + "offset=" + url.QueryEscape(page.Offset)
		default:
			next = ""
		}
	}
	return out, nil
}

// List returns every entity of a kind. Targets are collected per upstream since
// the Admin API only exposes them nested.
func (c *Client) List(ctx context.Context, kind string) ([]Entity, error) {
	if kind != "targets" {
		return c.list(ctx, "/"+kind+"?size=1000")
	}
	ups, err := c.list(ctx, "/upstreams?size=1000")
	if err != nil {
		return nil, err
	}
	out := []Entity{}
	for _, u := range ups {
		ts, err := c.list(ctx, "/upstreams/"+u.ID()+"/targets?size=1000")
		if err != nil {
			return nil, err
		}
		for _, t := range ts {
			if t["upstream"] == nil {
				t["upstream"] = map[string]any{"id": u.ID()}
			}
			out = append(out, t)
		}
	}
	return out, nil
}

func collectionPath(kind string, e Entity) (string, error) {
	if kind != "targets" {
		return "/" + kind, nil
	}
	up := e.RefID("upstream")
	if up == "" {
		return "", fmt.Errorf("target requires an upstream reference")
	}
	return "/upstreams/" + up + "/targets", nil
}

func (c *Client) Create(ctx context.Context, kind string, e Entity) (Entity, error) {
	path, err := collectionPath(kind, e)
	if err != nil {
		return nil, err
	}
	var out Entity
	if err := c.do(ctx, http.MethodPost, path, e, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Update uses PATCH, which Kong treats as a partial update — the caller sends
// only the fields it wants changed.
func (c *Client) Update(ctx context.Context, kind, id string, e Entity) (Entity, error) {
	if kind == "targets" {
		// Targets are immutable in Kong: replace by delete + create.
		if err := c.Delete(ctx, kind, id, e); err != nil {
			return nil, err
		}
		return c.Create(ctx, kind, e)
	}
	var out Entity
	if err := c.do(ctx, http.MethodPatch, "/"+kind+"/"+id, e, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) Delete(ctx context.Context, kind, id string, e Entity) error {
	path := "/" + kind + "/" + id
	if kind == "targets" {
		up := e.RefID("upstream")
		if up == "" {
			return fmt.Errorf("target %s requires an upstream reference to delete", id)
		}
		path = "/upstreams/" + up + "/targets/" + id
	}
	err := c.do(ctx, http.MethodDelete, path, nil, nil)
	var apiErr *APIError
	if err != nil && asAPIError(err, &apiErr) && apiErr.Status == http.StatusNotFound {
		return nil // already gone — deleting is idempotent for our purposes
	}
	return err
}

func asAPIError(err error, target **APIError) bool {
	if e, ok := err.(*APIError); ok {
		*target = e
		return true
	}
	return false
}

// Schema returns a raw Kong schema document, e.g. `plugins/rate-limiting` or
// `services`. The UI renders its property forms from it, which is what keeps
// custom plugins and version differences working without code changes.
func (c *Client) Schema(ctx context.Context, path string) (map[string]any, error) {
	var out map[string]any
	if err := c.do(ctx, http.MethodGet, "/schemas/"+strings.TrimLeft(path, "/"), nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Workspaces lists Enterprise workspaces; returns nil on OSS (404).
func (c *Client) Workspaces(ctx context.Context) ([]Entity, error) {
	root := strings.TrimRight(c.cfg.AdminURL, "/")
	var page listPage
	if err := c.do(ctx, http.MethodGet, root+"/workspaces", nil, &page); err != nil {
		var apiErr *APIError
		if asAPIError(err, &apiErr) && (apiErr.Status == 404 || apiErr.Status == 401 || apiErr.Status == 403) {
			return nil, nil
		}
		return nil, err
	}
	return page.Data, nil
}
