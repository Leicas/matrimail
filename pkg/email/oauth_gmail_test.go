package email

import (
	"context"
	"encoding/json"
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
// supplied test server, restoring them on test cleanup.
func withFakeGoogle(t *testing.T, srv *httptest.Server) {
	t.Helper()
	prevDevice, prevToken := googleDeviceCodeURL, googleTokenURL
	googleDeviceCodeURL = srv.URL + "/device/code"
	googleTokenURL = srv.URL + "/token"
	t.Cleanup(func() {
		googleDeviceCodeURL = prevDevice
		googleTokenURL = prevToken
	})
}

func TestDeviceCodeStart_Success(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/device/code", func(w http.ResponseWriter, r *http.Request) {
		if got := r.FormValue("client_id"); got != "test-client" {
			t.Errorf("client_id = %q, want test-client", got)
		}
		if got := r.FormValue("scope"); got != GmailFullScope {
			t.Errorf("scope = %q, want %q", got, GmailFullScope)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(DeviceCodeResponse{
			DeviceCode:      "DC123",
			UserCode:        "ABCD-EFGH",
			VerificationURL: "https://example.com/device",
			ExpiresIn:       1800,
			Interval:        5,
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	withFakeGoogle(t, srv)

	dcr, err := DeviceCodeStart(context.Background(), GmailOAuthConfig{ClientID: "test-client"})
	if err != nil {
		t.Fatalf("DeviceCodeStart: %v", err)
	}
	if dcr.UserCode != "ABCD-EFGH" || dcr.DeviceCode != "DC123" {
		t.Errorf("unexpected response: %+v", dcr)
	}
}

func TestDeviceCodeStart_MissingClientID(t *testing.T) {
	if _, err := DeviceCodeStart(context.Background(), GmailOAuthConfig{}); err == nil {
		t.Errorf("expected error for empty client_id")
	}
}

func TestDeviceCodeStart_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"forbidden"}`))
	}))
	defer srv.Close()
	withFakeGoogle(t, srv)

	if _, err := DeviceCodeStart(context.Background(), GmailOAuthConfig{ClientID: "x"}); err == nil {
		t.Errorf("expected error for non-200 response")
	}
}

// TestDeviceCodePoll_AllErrorBranches drives the polling state machine through
// each documented error response by serving a queue of canned replies. We use
// a tiny interval (1s, the smallest the loop accepts via dcr.Interval) and
// rotate responses per request so each poll iteration sees a different state.
func TestDeviceCodePoll_AllErrorBranches(t *testing.T) {
	t.Parallel()

	type tokenResponse struct {
		Status int
		Body   string
	}

	cases := []struct {
		name      string
		responses []tokenResponse
		wantErr   string // substring match
		wantOK    bool
	}{
		{
			name: "success on first poll",
			responses: []tokenResponse{
				{200, `{"access_token":"AT","refresh_token":"RT","token_type":"Bearer","expires_in":3600}`},
			},
			wantOK: true,
		},
		{
			name: "authorization_pending then success",
			responses: []tokenResponse{
				{200, `{"error":"authorization_pending"}`},
				{200, `{"access_token":"AT2","refresh_token":"RT2","token_type":"Bearer","expires_in":3600}`},
			},
			wantOK: true,
		},
		{
			name: "slow_down then success",
			responses: []tokenResponse{
				{200, `{"error":"slow_down"}`},
				{200, `{"access_token":"AT3","token_type":"Bearer","expires_in":3600}`},
			},
			wantOK: true,
		},
		{
			name:      "expired_token",
			responses: []tokenResponse{{200, `{"error":"expired_token"}`}},
			wantErr:   "expired",
		},
		{
			name:      "access_denied",
			responses: []tokenResponse{{200, `{"error":"access_denied"}`}},
			wantErr:   "denied",
		},
		{
			name:      "unknown error",
			responses: []tokenResponse{{200, `{"error":"invalid_grant","error_description":"bad code"}`}},
			wantErr:   "invalid_grant",
		},
		{
			name:      "empty access token with no error string",
			responses: []tokenResponse{{200, `{"token_type":"Bearer"}`}},
			wantErr:   "empty access token",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			i := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if i >= len(tc.responses) {
					// Should not happen if the test is set up right.
					t.Errorf("server hit more than expected (%d responses queued)", len(tc.responses))
					w.WriteHeader(500)
					return
				}
				resp := tc.responses[i]
				i++
				w.WriteHeader(resp.Status)
				_, _ = w.Write([]byte(resp.Body))
			}))
			defer srv.Close()
			withFakeGoogle(t, srv)

			dcr := &DeviceCodeResponse{
				DeviceCode: "DEVCODE",
				ExpiresIn:  60,
				Interval:   1, // poll every 1s; tests still finish fast
			}
			cfg := GmailOAuthConfig{ClientID: "id", ClientSecret: "sec"}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			tok, err := DeviceCodePoll(ctx, cfg, dcr)
			if tc.wantOK {
				if err != nil {
					t.Fatalf("expected success, got error: %v", err)
				}
				if tok == nil || tok.AccessToken == "" {
					t.Errorf("expected non-empty token, got %+v", tok)
				}
			} else {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if tc.wantErr != "" && !contains(err.Error(), tc.wantErr) {
					t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
				}
			}
		})
	}
}

func TestDeviceCodePoll_ContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
	}))
	defer srv.Close()
	withFakeGoogle(t, srv)

	dcr := &DeviceCodeResponse{DeviceCode: "DC", ExpiresIn: 60, Interval: 1}
	cfg := GmailOAuthConfig{ClientID: "id", ClientSecret: "sec"}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	if _, err := DeviceCodePoll(ctx, cfg, dcr); err == nil {
		t.Errorf("expected context cancel error")
	}
}

func TestDeviceCodePoll_MissingCreds(t *testing.T) {
	if _, err := DeviceCodePoll(context.Background(), GmailOAuthConfig{}, &DeviceCodeResponse{}); err == nil {
		t.Errorf("expected error for missing creds")
	}
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

// contains is a tiny strings.Contains shim so we don't need an extra import.
func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
