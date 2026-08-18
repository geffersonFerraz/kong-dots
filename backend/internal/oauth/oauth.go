// Package oauth implements the client-credentials flow used to authenticate
// against Kong Admin APIs that sit behind an OAuth2 authorization server.
//
// The token request is the RFC 6749 §4.4 shape: a POST to the token URL whose
// `application/x-www-form-urlencoded` body carries `client_id`,
// `client_secret` and `grant_type=client_credentials`.
package oauth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	GrantType = "client_credentials"

	// defaultSkew renews slightly before the real expiry so a request cannot
	// leave with a token that dies in flight.
	defaultSkew = 30 * time.Second

	// defaultLifetime is assumed when the server omits expires_in.
	defaultLifetime = 60 * time.Second
)

type Config struct {
	TokenURL      string
	ClientID      string
	ClientSecret  string
	TLSSkipVerify bool
	Timeout       time.Duration
	Skew          time.Duration
	// HTTPClient overrides the transport, mainly for tests.
	HTTPClient *http.Client
}

func (c Config) valid() error {
	if strings.TrimSpace(c.TokenURL) == "" {
		return fmt.Errorf("oauth: token URL is required")
	}
	if !strings.HasPrefix(c.TokenURL, "http://") && !strings.HasPrefix(c.TokenURL, "https://") {
		return fmt.Errorf("oauth: token URL must start with http:// or https://")
	}
	if strings.TrimSpace(c.ClientID) == "" {
		return fmt.Errorf("oauth: client_id is required")
	}
	return nil
}

// fingerprint identifies the credentials a cached token belongs to, so changing
// any of them retires the cache instead of reusing a token minted for the old
// ones. The secret is hashed, never kept in the key.
func (c Config) fingerprint() string {
	sum := sha256.Sum256([]byte(c.TokenURL + "\x00" + c.ClientID + "\x00" + c.ClientSecret))
	return hex.EncodeToString(sum[:8])
}

// Token is one issued access token plus the moment it stops being usable.
type Token struct {
	AccessToken string    `json:"access_token"`
	TokenType   string    `json:"token_type"`
	Scope       string    `json:"scope"`
	ExpiresIn   int       `json:"expires_in"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// Kind is the token type to put in the Authorization header, defaulting to
// Bearer when the authorization server does not say otherwise.
func (t *Token) Kind() string {
	kind := strings.TrimSpace(t.TokenType)
	if kind == "" || strings.EqualFold(kind, "bearer") {
		return "Bearer" // some servers lowercase it
	}
	return kind
}

// Header renders the full Authorization value.
func (t *Token) Header() string { return t.Kind() + " " + t.AccessToken }

func (t *Token) valid(now time.Time) bool {
	return t != nil && t.AccessToken != "" && now.Before(t.ExpiresAt)
}

// TokenSource hands out a valid access token, minting a new one whenever the
// cached one has expired. It is safe for concurrent use: a snapshot fires six
// Admin API calls at once and they must not trigger six token requests.
type TokenSource struct {
	cfg Config
	mu  sync.Mutex
	tok *Token

	// now is swappable so tests can travel forward without sleeping.
	now func() time.Time
}

func NewTokenSource(cfg Config) *TokenSource {
	if cfg.Timeout == 0 {
		cfg.Timeout = 15 * time.Second
	}
	if cfg.Skew == 0 {
		cfg.Skew = defaultSkew
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: cfg.Timeout}
	}
	return &TokenSource{cfg: cfg, now: time.Now}
}

// Token returns a usable token, refreshing first when the cached one expired.
func (ts *TokenSource) Token(ctx context.Context) (*Token, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.tok.valid(ts.now()) {
		return ts.tok, nil
	}
	return ts.fetchLocked(ctx)
}

// Invalidate drops the cached token so the next call mints a fresh one.
func (ts *TokenSource) Invalidate() {
	ts.mu.Lock()
	ts.tok = nil
	ts.mu.Unlock()
}

// Refresh returns a token that is not `stale`, which is what a caller wants
// after the gateway rejected the token it was holding.
//
// It is deliberately scoped to the rejected token: a snapshot fires several
// Admin API calls at once, and if each of them blindly invalidated the cache
// they would mint a token apiece and could even discard one a sibling had just
// obtained. Here the first caller refreshes and the rest are handed its result.
func (ts *TokenSource) Refresh(ctx context.Context, stale string) (*Token, error) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if ts.tok != nil && ts.tok.AccessToken != stale && ts.tok.valid(ts.now()) {
		return ts.tok, nil
	}
	ts.tok = nil
	return ts.fetchLocked(ctx)
}

// Peek reports the cached token without contacting the authorization server.
func (ts *TokenSource) Peek() *Token {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.tok
}

func (ts *TokenSource) fetchLocked(ctx context.Context) (*Token, error) {
	if err := ts.cfg.valid(); err != nil {
		return nil, err
	}
	form := url.Values{}
	form.Set("grant_type", GrantType)
	form.Set("client_id", ts.cfg.ClientID)
	form.Set("client_secret", ts.cfg.ClientSecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ts.cfg.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := ts.cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oauth: token request failed: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("oauth: token endpoint returned %d: %s", resp.StatusCode, snippet(raw))
	}

	var body struct {
		AccessToken string          `json:"access_token"`
		TokenType   string          `json:"token_type"`
		Scope       string          `json:"scope"`
		ExpiresIn   json.RawMessage `json:"expires_in"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("oauth: token response is not JSON: %s", snippet(raw))
	}
	if body.AccessToken == "" {
		return nil, fmt.Errorf("oauth: token response carried no access_token: %s", snippet(raw))
	}

	lifetime := defaultLifetime
	seconds := parseExpiresIn(body.ExpiresIn)
	if seconds > 0 {
		lifetime = time.Duration(seconds) * time.Second
	}
	// Never let the skew push the expiry into the past for very short tokens.
	usable := lifetime - ts.cfg.Skew
	if usable <= 0 {
		usable = lifetime / 2
	}

	ts.tok = &Token{
		AccessToken: body.AccessToken,
		TokenType:   body.TokenType,
		Scope:       body.Scope,
		ExpiresIn:   seconds,
		ExpiresAt:   ts.now().Add(usable),
	}
	return ts.tok, nil
}

// parseExpiresIn accepts both the numeric form and the quoted string some
// authorization servers emit.
func parseExpiresIn(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var n int
	if err := json.Unmarshal(raw, &n); err == nil {
		return n
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil {
			return n
		}
	}
	return 0
}

func snippet(raw []byte) string {
	s := strings.TrimSpace(string(raw))
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// Registry keeps one TokenSource per registered Kong so a token survives across
// HTTP requests instead of being minted for every call.
type Registry struct {
	mu      sync.Mutex
	sources map[string]*registryEntry
}

type registryEntry struct {
	fingerprint string
	source      *TokenSource
}

func NewRegistry() *Registry {
	return &Registry{sources: map[string]*registryEntry{}}
}

// Source returns the cached TokenSource for key, rebuilding it when the
// credentials changed.
func (r *Registry) Source(key string, cfg Config) *TokenSource {
	r.mu.Lock()
	defer r.mu.Unlock()
	fp := cfg.fingerprint()
	if e, ok := r.sources[key]; ok && e.fingerprint == fp {
		return e.source
	}
	source := NewTokenSource(cfg)
	r.sources[key] = &registryEntry{fingerprint: fp, source: source}
	return source
}

// Forget drops any cached token for key, e.g. when the connection is deleted.
func (r *Registry) Forget(key string) {
	r.mu.Lock()
	delete(r.sources, key)
	r.mu.Unlock()
}
