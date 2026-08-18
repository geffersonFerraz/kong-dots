package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// oauthEnv is a fake authorization server plus an Admin API that refuses
// anything but the token it was told to accept.
type oauthEnv struct {
	idp        *httptest.Server
	kong       *httptest.Server
	minted     int32
	adminCalls int32

	mu       sync.Mutex
	accepted string
	lastAuth string
}

func newOAuthEnv(t *testing.T) *oauthEnv {
	t.Helper()
	env := &oauthEnv{accepted: "token-1"}

	env.idp = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" ||
			r.PostFormValue("client_id") == "" || r.PostFormValue("grant_type") != "client_credentials" {
			http.Error(w, `{"error":"invalid_request"}`, http.StatusBadRequest)
			return
		}
		if r.PostFormValue("client_secret") != "s3cr3t" {
			http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
			return
		}
		n := atomic.AddInt32(&env.minted, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": fmt.Sprintf("token-%d", n),
			"token_type":   "Bearer",
			"scope":        "kong:admin",
			"expires_in":   3600,
		})
	}))
	t.Cleanup(env.idp.Close)

	env.kong = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&env.adminCalls, 1)
		got := r.Header.Get("Authorization")
		env.mu.Lock()
		env.lastAuth = got
		want := "Bearer " + env.accepted
		env.mu.Unlock()
		if got != want {
			http.Error(w, `{"message":"Unauthorized"}`, http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/":
			writeJSONTest(w, map[string]any{
				"version": "3.9.1", "configuration": map[string]any{"edition": "community"},
				"plugins": map[string]any{"available_on_server": map[string]any{}},
			})
		default:
			writeJSONTest(w, map[string]any{"data": []any{}})
		}
	}))
	t.Cleanup(env.kong.Close)
	return env
}

func (e *oauthEnv) auth() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastAuth
}

func (e *oauthEnv) onlyAccept(token string) {
	e.mu.Lock()
	e.accepted = token
	e.mu.Unlock()
}

func (e *oauthEnv) connectionBody() map[string]any {
	return map[string]any{
		"name": "oauth-kong", "admin_api_url": e.kong.URL, "auth_type": "oauth2",
		"oauth_token_url": e.idp.URL, "oauth_client_id": "kong-dots", "oauth_client_secret": "s3cr3t",
	}
}

func TestOAuthConnectionValidation(t *testing.T) {
	h := newTestServer(t)
	for _, tc := range []struct {
		name string
		body map[string]any
		want string
	}{
		{"no token url", map[string]any{"name": "x", "admin_api_url": "http://k", "auth_type": "oauth2", "oauth_client_id": "id"}, "oauth_token_url"},
		{"token url without scheme", map[string]any{"name": "x", "admin_api_url": "http://k", "auth_type": "oauth2", "oauth_token_url": "idp/token", "oauth_client_id": "id"}, "http://"},
		{"no client id", map[string]any{"name": "x", "admin_api_url": "http://k", "auth_type": "oauth2", "oauth_token_url": "https://idp/token"}, "oauth_client_id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, body := do(t, h, http.MethodPost, "/api/connections", tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d %s", rec.Code, rec.Body)
			}
			if !strings.Contains(fmt.Sprint(body["error"]), tc.want) {
				t.Errorf("error should mention %q: %v", tc.want, body["error"])
			}
		})
	}
}

func TestOAuthConnectionNeverReturnsTheClientSecret(t *testing.T) {
	h := newTestServer(t)
	env := newOAuthEnv(t)

	rec, created := do(t, h, http.MethodPost, "/api/connections", env.connectionBody())
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	if _, leaked := created["oauth_client_secret"]; leaked {
		t.Errorf("the client secret must never come back: %v", created)
	}
	if created["has_oauth_secret"] != true {
		t.Errorf("has_oauth_secret should be true: %v", created)
	}
	if created["oauth_client_id"] != "kong-dots" || created["oauth_token_url"] != env.idp.URL {
		t.Errorf("oauth settings not stored: %v", created)
	}

	// Renaming without resending the secret keeps it usable.
	body := env.connectionBody()
	body["name"] = "renamed"
	delete(body, "oauth_client_secret")
	rec, _ = do(t, h, http.MethodPut, "/api/connections/"+created["id"].(string), body)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}
	rec, _ = do(t, h, http.MethodGet, "/api/connections/"+created["id"].(string)+"/state", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("state after rename: %d %s", rec.Code, rec.Body)
	}
}

func TestTestConnectionReportsTheTokenItObtained(t *testing.T) {
	h := newTestServer(t)
	env := newOAuthEnv(t)

	rec, body := do(t, h, http.MethodPost, "/api/connections/test", env.connectionBody())
	if rec.Code != http.StatusOK {
		t.Fatalf("test: %d %s", rec.Code, rec.Body)
	}
	if body["ok"] != true {
		t.Fatalf("expected a successful test: %v", body)
	}
	tok := body["oauth"].(map[string]any)
	if tok["token_type"] != "Bearer" || tok["scope"] != "kong:admin" || tok["expires_in"] != float64(3600) {
		t.Errorf("token details not reported: %v", tok)
	}
}

func TestTestConnectionSeparatesAnAuthFailureFromAGatewayFailure(t *testing.T) {
	h := newTestServer(t)
	env := newOAuthEnv(t)

	bad := env.connectionBody()
	bad["oauth_client_secret"] = "wrong"
	rec, body := do(t, h, http.MethodPost, "/api/connections/test", bad)
	if rec.Code != http.StatusOK || body["ok"] != false {
		t.Fatalf("expected a reported failure: %d %v", rec.Code, body)
	}
	if body["stage"] != "oauth" {
		t.Errorf("a bad client secret is an oauth-stage failure: %v", body)
	}
	if !strings.Contains(fmt.Sprint(body["error"]), "invalid_client") {
		t.Errorf("the authorization server's message should reach the user: %v", body["error"])
	}
}

func TestOAuthTokenIsSharedAcrossRequests(t *testing.T) {
	h := newTestServer(t)
	env := newOAuthEnv(t)
	_, created := do(t, h, http.MethodPost, "/api/connections", env.connectionBody())
	id := created["id"].(string)

	for i := 0; i < 3; i++ {
		rec, _ := do(t, h, http.MethodGet, "/api/connections/"+id+"/state", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("state %d: %d", i, rec.Code)
		}
	}
	if env.auth() != "Bearer token-1" {
		t.Errorf("Admin API calls should carry the token, saw %q", env.auth())
	}
	// One token for many HTTP requests: the cache lives on the server, not per call.
	if got := atomic.LoadInt32(&env.minted); got != 1 {
		t.Fatalf("expected a single token to be minted, got %d", got)
	}
}

func TestRotatingTheClientSecretRetiresTheCachedToken(t *testing.T) {
	h := newTestServer(t)
	env := newOAuthEnv(t)
	_, created := do(t, h, http.MethodPost, "/api/connections", env.connectionBody())
	id := created["id"].(string)

	if rec, _ := do(t, h, http.MethodGet, "/api/connections/"+id+"/state", nil); rec.Code != http.StatusOK {
		t.Fatalf("first state: %d", rec.Code)
	}

	body := env.connectionBody()
	body["oauth_client_secret"] = "s3cr3t" // same value, but resent explicitly
	body["oauth_client_id"] = "kong-dots-rotated"
	if rec, _ := do(t, h, http.MethodPut, "/api/connections/"+id, body); rec.Code != http.StatusOK {
		t.Fatalf("update: %d", rec.Code)
	}

	env.onlyAccept("token-2")
	if rec, _ := do(t, h, http.MethodGet, "/api/connections/"+id+"/state", nil); rec.Code != http.StatusOK {
		t.Fatalf("state after rotation: %d", rec.Code)
	}
	if got := atomic.LoadInt32(&env.minted); got != 2 {
		t.Fatalf("changing the credentials must mint a new token, minted %d", got)
	}
}

// Kong rejecting a still-cached token must not surface as a failed refresh.
func TestExpiredTokenIsRenewedBeforeTheNextAction(t *testing.T) {
	h := newTestServer(t)
	env := newOAuthEnv(t)
	_, created := do(t, h, http.MethodPost, "/api/connections", env.connectionBody())
	id := created["id"].(string)

	if rec, _ := do(t, h, http.MethodGet, "/api/connections/"+id+"/state", nil); rec.Code != http.StatusOK {
		t.Fatalf("first state: %d", rec.Code)
	}
	env.onlyAccept("token-2") // the gateway revoked the first token early

	rec, _ := do(t, h, http.MethodGet, "/api/connections/"+id+"/state", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("the client should have re-minted and retried, got %d %s", rec.Code, rec.Body)
	}
	if env.auth() != "Bearer token-2" {
		t.Errorf("the retry should carry the new token, saw %q", env.auth())
	}
}

func TestStatusReportsTheLiveToken(t *testing.T) {
	h := newTestServer(t)
	env := newOAuthEnv(t)
	_, created := do(t, h, http.MethodPost, "/api/connections", env.connectionBody())
	id := created["id"].(string)

	rec, body := do(t, h, http.MethodGet, "/api/connections/"+id+"/status", nil)
	if rec.Code != http.StatusOK || body["ok"] != true {
		t.Fatalf("status: %d %v", rec.Code, body)
	}
	tok := body["oauth"].(map[string]any)
	if tok["token"] != true || tok["scope"] != "kong:admin" {
		t.Errorf("status should describe the cached token: %v", tok)
	}
}

func TestNonOAuthConnectionsReportNoToken(t *testing.T) {
	h := newTestServer(t)
	kong := newFakeKong(t)
	id := registerConnection(t, h, kong.srv.URL)

	_, body := do(t, h, http.MethodGet, "/api/connections/"+id+"/status", nil)
	if body["oauth"] != nil {
		t.Errorf("a key-auth connection has no token to report: %v", body["oauth"])
	}
}
