package email

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

// stubTokenSource lets sender_factory_test.go assert PickSender's branching
// without making real HTTP calls.
type stubTokenSource struct{}

func (stubTokenSource) Token() (*oauth2.Token, error) {
	return &oauth2.Token{AccessToken: "stub", Expiry: time.Now().Add(time.Hour)}, nil
}

// withFakeGoogle swaps the package-level Google endpoint URLs to point at the
// supplied test server, restoring them on test cleanup. Used by the OAuth and
// authcode tests to drive the flow against an httptest server instead of the
// real Google endpoints.
func withFakeGoogle(t *testing.T, srv *httptest.Server) {
	t.Helper()
	prevAuth, prevToken, prevRevoke := googleAuthURL, googleTokenURL, googleRevokeURL
	googleAuthURL = srv.URL + "/auth"
	googleTokenURL = srv.URL + "/token"
	googleRevokeURL = srv.URL + "/revoke"
	t.Cleanup(func() {
		googleAuthURL = prevAuth
		googleTokenURL = prevToken
		googleRevokeURL = prevRevoke
	})
}

func TestTokenSource_RefreshUsesConfiguredEndpoint(t *testing.T) {
	// A near-expired token should trigger a refresh hit on the test server.
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"REFRESHED","token_type":"Bearer","expires_in":3600}`))
	}))
	defer srv.Close()
	withFakeGoogle(t, srv)

	cfg := GmailOAuthConfig{ClientID: "id", ClientSecret: "sec"}
	expired := &oauth2.Token{
		AccessToken:  "OLD",
		RefreshToken: "RT",
		Expiry:       time.Now().Add(-time.Hour),
	}
	ts := TokenSource(context.Background(), cfg, expired)
	tok, err := ts.Token()
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if !called {
		t.Errorf("token endpoint was not hit during refresh")
	}
	if tok.AccessToken != "REFRESHED" {
		t.Errorf("AccessToken = %q, want REFRESHED", tok.AccessToken)
	}
}
