package kong

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gefferson/kong-flow/backend/internal/oauth"
)

// oauthKong pairs a fake authorization server with a fake Admin API that only
// accepts the token currently issued.
func oauthKong(t *testing.T) (*Client, *int32, *int32, func(string)) {
	t.Helper()
	var minted, adminCalls int32
	accepted := "token-1"

	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&minted, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": fmt.Sprintf("token-%d", n), "token_type": "Bearer", "expires_in": 3600,
		})
	}))
	t.Cleanup(idp.Close)

	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&adminCalls, 1)
		if r.Header.Get("Authorization") != "Bearer "+accepted {
			http.Error(w, `{"message":"Unauthorized"}`, http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"version": "3.9.1", "data": []any{}})
	}))
	t.Cleanup(admin.Close)

	source := oauth.NewTokenSource(oauth.Config{TokenURL: idp.URL, ClientID: "id", ClientSecret: "secret"})
	client := New(Config{AdminURL: admin.URL, AuthType: AuthOAuth2, Tokens: source})
	return client, &minted, &adminCalls, func(tok string) { accepted = tok }
}

func TestOAuthClientAuthorizesAndReusesTheToken(t *testing.T) {
	client, minted, _, _ := oauthKong(t)

	for i := 0; i < 3; i++ {
		if _, err := client.Info(context.Background()); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := atomic.LoadInt32(minted); got != 1 {
		t.Fatalf("the token should be minted once and reused, minted %d", got)
	}
}

// If Kong rejects a token the source still believed was fresh, the client must
// mint a new one and retry rather than surfacing a spurious 401.
func TestOAuthClientRetriesOnceAfterRejection(t *testing.T) {
	client, minted, adminCalls, rotate := oauthKong(t)
	if _, err := client.Info(context.Background()); err != nil {
		t.Fatal(err)
	}

	rotate("token-2") // the gateway now only accepts the next token
	if _, err := client.Info(context.Background()); err != nil {
		t.Fatalf("client did not recover from a rejected token: %v", err)
	}
	if got := atomic.LoadInt32(minted); got != 2 {
		t.Errorf("expected exactly one refresh, minted %d", got)
	}
	if got := atomic.LoadInt32(adminCalls); got != 3 {
		t.Errorf("expected the rejected call to be retried once, admin calls = %d", got)
	}
}

func TestOAuthClientGivesUpAfterTheRetry(t *testing.T) {
	// The gateway never accepts what the IdP issues.
	client, _, adminCalls, rotate := oauthKong(t)
	rotate("never-issued")

	_, err := client.Info(context.Background())
	if err == nil {
		t.Fatal("expected the 401 to surface after one retry")
	}
	var apiErr *APIError
	if !asAPIError(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
		t.Fatalf("expected a 401 APIError, got %v", err)
	}
	if got := atomic.LoadInt32(adminCalls); got != 2 {
		t.Errorf("expected exactly one retry, admin calls = %d", got)
	}
}

func TestOAuthWithoutATokenSourceFailsLoudly(t *testing.T) {
	client := New(Config{AdminURL: "http://kong.invalid", AuthType: AuthOAuth2})
	if _, err := client.Info(context.Background()); err == nil {
		t.Fatal("expected a configuration error")
	}
}

func TestStaticAuthSchemesSetTheirHeaders(t *testing.T) {
	for _, tc := range []struct {
		auth   AuthType
		header string
		value  string
		custom string
	}{
		{AuthKey, "apikey", "s3cr3t", ""},
		{AuthKey, "X-Custom-Key", "s3cr3t", "X-Custom-Key"},
		{AuthRBAC, "Kong-Admin-Token", "s3cr3t", ""},
		{AuthBearer, "Authorization", "Bearer s3cr3t", ""},
		{AuthBasic, "Authorization", "Basic s3cr3t", ""},
	} {
		var got string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = r.Header.Get(tc.header)
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "3.9.1"})
		}))
		client := New(Config{AdminURL: srv.URL, AuthType: tc.auth, Secret: "s3cr3t", AuthHeader: tc.custom})
		if _, err := client.Info(context.Background()); err != nil {
			t.Fatalf("%s: %v", tc.auth, err)
		}
		if got != tc.value {
			t.Errorf("%s: %s = %q, want %q", tc.auth, tc.header, got, tc.value)
		}
		srv.Close()
	}
}
