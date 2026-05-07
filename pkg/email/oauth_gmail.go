package email

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// GmailOAuthConfig holds the user-provided Google OAuth client credentials.
// User creates these in Google Cloud Console as a "Desktop app" OAuth 2.0 Client
// and pastes them into config.yaml under gmail_oauth:.
type GmailOAuthConfig struct {
	ClientID     string
	ClientSecret string
}

// GmailFullScope grants both Gmail API access AND IMAP/SMTP via XOAUTH2,
// eliminating the need for App Passwords on Gmail accounts.
const GmailFullScope = "https://mail.google.com/"

// Endpoints for Google's OAuth 2.0 for Limited-Input Devices flow. Made
// package-level vars so tests can swap them out for an httptest.Server.
var (
	googleDeviceCodeURL = "https://oauth2.googleapis.com/device/code"
	googleTokenURL      = "https://oauth2.googleapis.com/token"
	googleAuthURL       = "https://accounts.google.com/o/oauth2/auth"
)

// DeviceCodeResponse is the JSON shape returned from Google's device code endpoint.
type DeviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_url"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// DeviceCodeStart initiates the device-code flow; returns the codes the user
// must visit/enter. The caller is responsible for surfacing UserCode and
// VerificationURL to the user (via a Matrix DM in matrimail's case).
func DeviceCodeStart(ctx context.Context, cfg GmailOAuthConfig) (*DeviceCodeResponse, error) {
	if cfg.ClientID == "" {
		return nil, errors.New("oauth: missing gmail_oauth.client_id in config")
	}
	form := url.Values{}
	form.Set("client_id", cfg.ClientID)
	form.Set("scope", GmailFullScope)

	req, err := http.NewRequestWithContext(ctx, "POST", googleDeviceCodeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("device code start: %s: %s", resp.Status, body)
	}
	var dcr DeviceCodeResponse
	if err := json.Unmarshal(body, &dcr); err != nil {
		return nil, fmt.Errorf("device code parse: %w", err)
	}
	return &dcr, nil
}

// deviceTokenResp mirrors the JSON shape of Google's /token responses for the
// device-code grant, including the error fields.
type deviceTokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

// DeviceCodePoll polls Google's token endpoint until the user has authorized
// (or the device code expires). Returns the resulting oauth2.Token, including
// a refresh token the caller MUST persist alongside the per-account record.
func DeviceCodePoll(ctx context.Context, cfg GmailOAuthConfig, dcr *DeviceCodeResponse) (*oauth2.Token, error) {
	if dcr == nil {
		return nil, errors.New("oauth: nil device code response")
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, errors.New("oauth: missing client_id/client_secret for device code poll")
	}

	deadline := time.Now().Add(time.Duration(dcr.ExpiresIn) * time.Second)
	interval := time.Duration(dcr.Interval) * time.Second
	if interval == 0 {
		interval = 5 * time.Second
	}

	for {
		if time.Now().After(deadline) {
			return nil, errors.New("oauth device code expired before authorization")
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}

		tr, err := postDeviceTokenRequest(ctx, cfg, dcr.DeviceCode)
		if err != nil {
			return nil, err
		}

		switch tr.Error {
		case "":
			if tr.AccessToken == "" {
				return nil, fmt.Errorf("token endpoint returned empty access token (error_description=%q)", tr.ErrorDesc)
			}
			return &oauth2.Token{
				AccessToken:  tr.AccessToken,
				RefreshToken: tr.RefreshToken,
				TokenType:    tr.TokenType,
				Expiry:       time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second),
			}, nil
		case "authorization_pending":
			continue
		case "slow_down":
			interval += 5 * time.Second
			continue
		case "expired_token":
			return nil, errors.New("oauth device code expired")
		case "access_denied":
			return nil, errors.New("oauth: user denied access")
		default:
			return nil, fmt.Errorf("oauth device poll: %s: %s", tr.Error, tr.ErrorDesc)
		}
	}
}

// postDeviceTokenRequest is split out so unit tests can drive the polling
// state machine without sleeping for the interval each iteration.
func postDeviceTokenRequest(ctx context.Context, cfg GmailOAuthConfig, deviceCode string) (*deviceTokenResp, error) {
	form := url.Values{}
	form.Set("client_id", cfg.ClientID)
	form.Set("client_secret", cfg.ClientSecret)
	form.Set("device_code", deviceCode)
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")

	req, err := http.NewRequestWithContext(ctx, "POST", googleTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var tr deviceTokenResp
	// Body may be empty on a transport hiccup; tolerate that and return an empty struct.
	_ = json.Unmarshal(body, &tr)
	return &tr, nil
}

// TokenSource returns an auto-refreshing oauth2.TokenSource for the given saved
// token. Uses the standard oauth2 library, which calls the token URL with the
// stored refresh token whenever the access token is within ~10s of expiry.
func TokenSource(ctx context.Context, cfg GmailOAuthConfig, tok *oauth2.Token) oauth2.TokenSource {
	oauthCfg := &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		Endpoint: oauth2.Endpoint{
			AuthURL:  googleAuthURL,
			TokenURL: googleTokenURL,
		},
		Scopes: []string{GmailFullScope},
	}
	return oauthCfg.TokenSource(ctx, tok)
}
