package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// tokenServer records every token request so the exact wire contract can be
// asserted, and hands out a fresh token each time.
type tokenServer struct {
	srv      *httptest.Server
	requests []recordedRequest
	calls    int32
	mu       sync.Mutex

	status    int
	body      string
	expiresIn any
}

func newTokenServer(t *testing.T) *tokenServer {
	t.Helper()
	ts := &tokenServer{status: http.StatusOK, expiresIn: 3600}
	ts.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		ts.mu.Lock()
		ts.requests = append(ts.requests, recordedRequest{
			method: r.Method, header: r.Header.Clone(), form: r.PostForm,
		})
		ts.mu.Unlock()
		n := atomic.AddInt32(&ts.calls, 1)

		w.Header().Set("Content-Type", "application/json")
		if ts.status >= 400 {
			w.WriteHeader(ts.status)
			fmt.Fprint(w, ts.body)
			return
		}
		if ts.body != "" {
			fmt.Fprint(w, ts.body)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": fmt.Sprintf("token-%d", n),
			"token_type":   "bearer",
			"scope":        "kong:admin",
			"expires_in":   ts.expiresIn,
		})
	}))
	t.Cleanup(ts.srv.Close)
	return ts
}

// recordedRequest keeps what the token endpoint received, body included.
type recordedRequest struct {
	method string
	header http.Header
	form   url.Values
}

func (ts *tokenServer) lastRequest(t *testing.T) recordedRequest {
	t.Helper()
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if len(ts.requests) == 0 {
		t.Fatal("the token endpoint was never called")
	}
	return ts.requests[len(ts.requests)-1]
}

func sourceFor(ts *tokenServer) *TokenSource {
	return NewTokenSource(Config{TokenURL: ts.srv.URL, ClientID: "kong-dots", ClientSecret: "s3cr3t"})
}

func TestTokenRequestFollowsTheAgreedContract(t *testing.T) {
	ts := newTokenServer(t)
	tok, err := sourceFor(ts).Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	req := ts.lastRequest(t)
	if req.method != http.MethodPost {
		t.Errorf("token request must be a POST, got %s", req.method)
	}
	if got := req.header.Get("Content-Type"); got != "application/x-www-form-urlencoded" {
		t.Errorf("content-type = %q", got)
	}
	// RFC 6749 §4.4: the credentials and the grant travel in the form body.
	if got := req.form.Get("client_id"); got != "kong-dots" {
		t.Errorf("client_id = %q", got)
	}
	if got := req.form.Get("client_secret"); got != "s3cr3t" {
		t.Errorf("client_secret = %q", got)
	}
	if got := req.form.Get("grant_type"); got != "client_credentials" {
		t.Errorf("grant_type = %q", got)
	}
	// And nowhere else: a credential in a header is a credential in a proxy log.
	for _, name := range []string{"client_id", "client_secret", "grant_type", "Authorization"} {
		if got := req.header.Get(name); got != "" {
			t.Errorf("%s must not be sent as a header, got %q", name, got)
		}
	}

	if tok.AccessToken != "token-1" || tok.Scope != "kong:admin" {
		t.Errorf("token not parsed: %+v", tok)
	}
	if tok.Header() != "Bearer token-1" {
		t.Errorf("authorization header = %q", tok.Header())
	}
}

func TestTokenIsReusedUntilItExpires(t *testing.T) {
	ts := newTokenServer(t)
	src := sourceFor(ts)

	for i := 0; i < 5; i++ {
		if _, err := src.Token(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if got := atomic.LoadInt32(&ts.calls); got != 1 {
		t.Fatalf("a valid token must be reused, the endpoint was hit %d times", got)
	}
}

func TestExpiredTokenIsMintedAgain(t *testing.T) {
	ts := newTokenServer(t)
	src := sourceFor(ts)

	now := time.Now()
	src.now = func() time.Time { return now }
	first, err := src.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// Still inside the window: same token.
	now = now.Add(time.Hour - 2*time.Minute)
	again, _ := src.Token(context.Background())
	if again.AccessToken != first.AccessToken {
		t.Fatalf("token replaced too early: %s -> %s", first.AccessToken, again.AccessToken)
	}

	// Past expiry (minus skew): a new one.
	now = now.Add(3 * time.Minute)
	renewed, err := src.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if renewed.AccessToken == first.AccessToken {
		t.Fatal("expired token was reused")
	}
	if got := atomic.LoadInt32(&ts.calls); got != 2 {
		t.Fatalf("expected exactly one refresh, endpoint hit %d times", got)
	}
}

func TestSkewRenewsBeforeTheRealExpiry(t *testing.T) {
	ts := newTokenServer(t)
	src := NewTokenSource(Config{TokenURL: ts.srv.URL, ClientID: "id", Skew: 30 * time.Second})
	now := time.Now()
	src.now = func() time.Time { return now }
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	// 3590s in: the token is technically alive for 10 more seconds, but the
	// skew must already have retired it.
	now = now.Add(3590 * time.Second)
	if _, err := src.Token(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&ts.calls); got != 2 {
		t.Fatalf("skew did not trigger an early refresh (calls=%d)", got)
	}
}

func TestShortLivedTokenStillGetsAUsableWindow(t *testing.T) {
	ts := newTokenServer(t)
	ts.expiresIn = 10 // shorter than the default skew
	src := sourceFor(ts)
	now := time.Now()
	src.now = func() time.Time { return now }

	tok, err := src.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !tok.ExpiresAt.After(now) {
		t.Fatalf("a 10s token must still be usable, expires_at=%v now=%v", tok.ExpiresAt, now)
	}
}

func TestExpiresInAcceptsAStringAndCanBeAbsent(t *testing.T) {
	ts := newTokenServer(t)
	ts.body = `{"access_token":"abc","token_type":"Bearer","expires_in":"120"}`
	src := sourceFor(ts)
	now := time.Now()
	src.now = func() time.Time { return now }
	tok, err := src.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok.ExpiresIn != 120 {
		t.Errorf("string expires_in not parsed: %+v", tok)
	}

	ts.body = `{"access_token":"no-expiry","token_type":"Bearer"}`
	src2 := sourceFor(ts)
	src2.now = func() time.Time { return now }
	tok2, err := src2.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !tok2.ExpiresAt.After(now) || tok2.ExpiresAt.After(now.Add(2*time.Minute)) {
		t.Errorf("a missing expires_in should fall back to a short window, got %v", tok2.ExpiresAt.Sub(now))
	}
}

func TestTokenErrorsAreExplicit(t *testing.T) {
	t.Run("http error surfaces the body", func(t *testing.T) {
		ts := newTokenServer(t)
		ts.status = http.StatusUnauthorized
		ts.body = `{"error":"invalid_client"}`
		_, err := sourceFor(ts).Token(context.Background())
		if err == nil || !strings.Contains(err.Error(), "invalid_client") || !strings.Contains(err.Error(), "401") {
			t.Fatalf("unhelpful error: %v", err)
		}
	})

	t.Run("missing access_token", func(t *testing.T) {
		ts := newTokenServer(t)
		ts.body = `{"token_type":"Bearer","expires_in":60}`
		_, err := sourceFor(ts).Token(context.Background())
		if err == nil || !strings.Contains(err.Error(), "no access_token") {
			t.Fatalf("unhelpful error: %v", err)
		}
	})

	t.Run("not JSON", func(t *testing.T) {
		ts := newTokenServer(t)
		ts.body = `<html>gateway timeout</html>`
		_, err := sourceFor(ts).Token(context.Background())
		if err == nil || !strings.Contains(err.Error(), "not JSON") {
			t.Fatalf("unhelpful error: %v", err)
		}
	})

	t.Run("missing configuration", func(t *testing.T) {
		for _, cfg := range []Config{
			{ClientID: "x"},
			{TokenURL: "not-a-url", ClientID: "x"},
			{TokenURL: "https://idp/token"},
		} {
			if _, err := NewTokenSource(cfg).Token(context.Background()); err == nil {
				t.Errorf("expected a validation error for %+v", cfg)
			}
		}
	})
}

func TestInvalidateForcesTheNextCallToRefresh(t *testing.T) {
	ts := newTokenServer(t)
	src := sourceFor(ts)
	first, _ := src.Token(context.Background())
	src.Invalidate()
	if src.Peek() != nil {
		t.Fatal("Invalidate must drop the cached token")
	}
	second, _ := src.Token(context.Background())
	if second.AccessToken == first.AccessToken {
		t.Fatal("token was not re-minted after Invalidate")
	}
}

// A snapshot fires six Admin API calls at once; they must share one token.
func TestConcurrentCallersMintOneToken(t *testing.T) {
	ts := newTokenServer(t)
	src := sourceFor(ts)

	var wg sync.WaitGroup
	tokens := make([]string, 12)
	for i := range tokens {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tok, err := src.Token(context.Background())
			if err != nil {
				t.Error(err)
				return
			}
			tokens[i] = tok.AccessToken
		}(i)
	}
	wg.Wait()

	if got := atomic.LoadInt32(&ts.calls); got != 1 {
		t.Fatalf("expected a single token request, got %d", got)
	}
	for _, tok := range tokens {
		if tok != tokens[0] {
			t.Fatalf("callers saw different tokens: %v", tokens)
		}
	}
}

func TestRegistryReusesAndRetiresSources(t *testing.T) {
	ts := newTokenServer(t)
	reg := NewRegistry()
	cfg := Config{TokenURL: ts.srv.URL, ClientID: "id", ClientSecret: "one"}

	a := reg.Source("conn-1", cfg)
	if b := reg.Source("conn-1", cfg); a != b {
		t.Fatal("the same credentials must reuse the cached source")
	}
	if _, err := a.Token(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Rotating the secret must not keep serving the token minted for the old one.
	rotated := cfg
	rotated.ClientSecret = "two"
	c := reg.Source("conn-1", rotated)
	if c == a {
		t.Fatal("changing the client secret must retire the cached source")
	}
	if c.Peek() != nil {
		t.Fatal("the new source must start without a token")
	}

	if other := reg.Source("conn-2", cfg); other == a {
		t.Fatal("different connections must not share a token")
	}

	reg.Forget("conn-1")
	if again := reg.Source("conn-1", rotated); again == c {
		t.Fatal("Forget must drop the cached source")
	}
}

// Regression: several Admin API calls run concurrently, so a rejected token can
// be reported by many callers at once. Only one replacement may be minted, and
// no caller may discard a token a sibling just obtained.
func TestConcurrentRefreshMintsOneReplacement(t *testing.T) {
	ts := newTokenServer(t)
	src := sourceFor(ts)

	first, err := src.Token(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	got := make([]string, 8)
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tok, err := src.Refresh(context.Background(), first.AccessToken)
			if err != nil {
				t.Error(err)
				return
			}
			got[i] = tok.AccessToken
		}(i)
	}
	wg.Wait()

	if calls := atomic.LoadInt32(&ts.calls); calls != 2 {
		t.Fatalf("expected one initial token plus one replacement, endpoint hit %d times", calls)
	}
	for _, tok := range got {
		if tok != got[0] || tok == first.AccessToken {
			t.Fatalf("callers disagreed on the replacement: %v (was %s)", got, first.AccessToken)
		}
	}
}

func TestRefreshKeepsATokenSomeoneElseAlreadyReplaced(t *testing.T) {
	ts := newTokenServer(t)
	src := sourceFor(ts)

	stale, _ := src.Token(context.Background())
	replaced, err := src.Refresh(context.Background(), stale.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	// A late caller still holding the stale token must be handed the existing
	// replacement, not trigger another round trip.
	again, err := src.Refresh(context.Background(), stale.AccessToken)
	if err != nil {
		t.Fatal(err)
	}
	if again.AccessToken != replaced.AccessToken {
		t.Fatalf("expected the existing replacement, got %s want %s", again.AccessToken, replaced.AccessToken)
	}
	if calls := atomic.LoadInt32(&ts.calls); calls != 2 {
		t.Fatalf("no extra token should have been minted, endpoint hit %d times", calls)
	}
}
