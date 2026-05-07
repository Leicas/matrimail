package email

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
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

// Gmail OAuth scopes.
//
// matrimail picks one of two "modes" at login time:
//
//   - ScopeModeModify (default): Gmail API only. The scope set is
//     gmail.modify + gmail.send. These are SENSITIVE scopes — verification is
//     required to publish the project to "In production", but no CASA
//     assessment, so a self-hosted bridge can realistically reach long-lived
//     refresh tokens. Used for inbound via users.history.list polling and
//     outbound via Users.Messages.Send.
//
//   - ScopeModeFull (advanced): mail.google.com. This single scope grants both
//     Gmail API access AND IMAP/SMTP via XOAUTH2. It is a RESTRICTED scope —
//     production publishing requires verification + CASA Tier 2 assessment
//     ($500–$4,500/yr, recurring), which is out of reach for a self-hosted
//     FOSS bridge. In practice the project stays in "Testing" mode forever and
//     refresh tokens expire after 7 days. Use only when IMAP semantics are
//     required (e.g. Workspace admin disabled the Gmail API).
const (
	GmailFullScope     = "https://mail.google.com/"                // restricted; covers IMAP+SMTP XOAUTH2
	GmailModifyScope   = "https://www.googleapis.com/auth/gmail.modify"
	GmailSendScope     = "https://www.googleapis.com/auth/gmail.send"
	GmailReadonlyScope = "https://www.googleapis.com/auth/gmail.readonly"
)

// AuthCodeFlow scope-mode identifiers (kept in this package so non-connector
// callers don't have to import the whole connector package). Mirrors
// connector.ScopeMode{Modify,Full}.
const (
	AuthCodeScopeModeModify = "modify"
	AuthCodeScopeModeFull   = "full"
)

// ScopesForMode returns the OAuth scope list for the given scope-mode string.
// Unknown values fall back to ScopeModeModify (the recommended default) so
// misconfigured callers fail closed into the safer mode rather than the
// restricted one.
func ScopesForMode(mode string) []string {
	switch mode {
	case AuthCodeScopeModeFull:
		return []string{GmailFullScope}
	case AuthCodeScopeModeModify:
		return []string{GmailModifyScope, GmailSendScope}
	default:
		return []string{GmailModifyScope, GmailSendScope}
	}
}

// GeneratePKCE returns a (verifier, challenge) pair per RFC 7636.
//
// The verifier is 64 random bytes base64url-encoded (~86 chars), well within
// the 43–128 range the spec requires. The challenge is the SHA-256 hash of the
// verifier, base64url-encoded without padding. Method is always S256 — no
// caller should ever fall back to "plain".
func GeneratePKCE() (verifier, challenge string, err error) {
	raw := make([]byte, 64)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", "", fmt.Errorf("pkce: read random: %w", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(sum[:])
	return verifier, challenge, nil
}

// GenerateState returns 32 random bytes base64url-encoded for use as the
// `state` parameter. The login flow stores this server-side and rejects any
// callback whose state doesn't match — protects against CSRF on the loopback
// listener and against another local user racing the callback.
func GenerateState() (string, error) {
	raw := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return "", fmt.Errorf("state: read random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// BuildAuthURL constructs the URL the user opens in their browser to authorize
// matrimail. Forces:
//   - access_type=offline so we get a refresh token,
//   - prompt=consent so a second login on a different scope re-shows the
//     consent screen and Google re-issues a refresh token (Google only emits
//     refresh tokens on the first consent unless re-prompted),
//   - PKCE S256 challenge,
//   - login_hint pre-fills the email field on the Google sign-in page.
//
// The returned URL is safe to display in a Matrix DM; nothing in it is secret
// (the verifier is server-side only).
func BuildAuthURL(cfg GmailOAuthConfig, redirectURI, state, codeChallenge, loginHint string, scopes []string) (string, error) {
	if cfg.ClientID == "" {
		return "", errors.New("oauth: missing gmail_oauth.client_id")
	}
	if redirectURI == "" {
		return "", errors.New("oauth: missing redirectURI")
	}
	if codeChallenge == "" {
		return "", errors.New("oauth: missing PKCE code_challenge")
	}
	if state == "" {
		return "", errors.New("oauth: missing state")
	}
	if len(scopes) == 0 {
		scopes = ScopesForMode(AuthCodeScopeModeModify)
	}
	q := url.Values{}
	q.Set("client_id", cfg.ClientID)
	q.Set("redirect_uri", redirectURI)
	q.Set("response_type", "code")
	q.Set("scope", strings.Join(scopes, " "))
	q.Set("access_type", "offline")
	q.Set("prompt", "consent")
	q.Set("state", state)
	q.Set("code_challenge", codeChallenge)
	q.Set("code_challenge_method", "S256")
	if loginHint != "" {
		q.Set("login_hint", loginHint)
	}
	return googleAuthURL + "?" + q.Encode(), nil
}

// authCodeTokenResp mirrors Google's /token JSON for the authorization-code
// grant, including refresh-token error fields.
type authCodeTokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

// ExchangeCode trades an authorization code for an oauth2.Token. Caller MUST
// pass back the same codeVerifier they sent in the auth URL (PKCE) and the
// exact same redirectURI (Google validates both).
//
// On success the returned token has both AccessToken and RefreshToken
// populated — if RefreshToken is empty Google didn't include one (usually
// because the user didn't re-consent) and the caller should treat this as a
// failure: an OAuth account without a refresh token would silently die in 1h.
func ExchangeCode(ctx context.Context, cfg GmailOAuthConfig, code, codeVerifier, redirectURI string) (*oauth2.Token, error) {
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, errors.New("oauth: missing client_id/client_secret")
	}
	if code == "" || codeVerifier == "" || redirectURI == "" {
		return nil, errors.New("oauth: ExchangeCode missing code/verifier/redirectURI")
	}
	form := url.Values{}
	form.Set("client_id", cfg.ClientID)
	form.Set("client_secret", cfg.ClientSecret)
	form.Set("code", code)
	form.Set("code_verifier", codeVerifier)
	form.Set("grant_type", "authorization_code")
	form.Set("redirect_uri", redirectURI)

	req, err := http.NewRequestWithContext(ctx, "POST", googleTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token exchange transport: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var tr authCodeTokenResp
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("token exchange parse (status %s): %w", resp.Status, err)
	}
	if tr.Error != "" {
		return nil, fmt.Errorf("token exchange: %s: %s", tr.Error, tr.ErrorDesc)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token exchange: %s: %s", resp.Status, body)
	}
	if tr.AccessToken == "" {
		return nil, errors.New("token exchange: empty access_token")
	}
	if tr.RefreshToken == "" {
		// Without a refresh token we'd silently die in ~1 hour. That's a
		// worse failure mode than a clear "please run !matrimail login again
		// with prompt=consent". Refuse to persist.
		return nil, errors.New("token exchange: no refresh_token returned (Google only emits one on first consent — try revoking the app at myaccount.google.com/permissions and logging in again)")
	}
	return &oauth2.Token{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		TokenType:    tr.TokenType,
		Expiry:       time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second),
	}, nil
}

// ExchangeRefreshToken does a one-shot refresh-token grant against Google's
// /token endpoint. Used by the !matrimail oauth paste-token admin command to
// validate a user-supplied refresh token before persisting it: a successful
// response confirms the token works and gives us an initial access token.
//
// Unlike TokenSource (which is the long-lived refresh-aware wrapper for an
// already-saved token), this is the bootstrap call.
func ExchangeRefreshToken(ctx context.Context, cfg GmailOAuthConfig, refreshToken string) (*oauth2.Token, error) {
	if cfg.ClientID == "" || cfg.ClientSecret == "" {
		return nil, errors.New("oauth: missing client_id/client_secret")
	}
	if refreshToken == "" {
		return nil, errors.New("oauth: empty refresh_token")
	}
	form := url.Values{}
	form.Set("client_id", cfg.ClientID)
	form.Set("client_secret", cfg.ClientSecret)
	form.Set("refresh_token", refreshToken)
	form.Set("grant_type", "refresh_token")

	req, err := http.NewRequestWithContext(ctx, "POST", googleTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("refresh transport: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	var tr authCodeTokenResp
	_ = json.Unmarshal(body, &tr)
	if tr.Error != "" {
		return nil, fmt.Errorf("refresh: %s: %s", tr.Error, tr.ErrorDesc)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("refresh: %s: %s", resp.Status, body)
	}
	if tr.AccessToken == "" {
		return nil, errors.New("refresh: empty access_token")
	}
	tok := &oauth2.Token{
		AccessToken: tr.AccessToken,
		// Google's refresh-token grant doesn't return a new refresh token —
		// the caller's existing one stays valid.
		RefreshToken: refreshToken,
		TokenType:    tr.TokenType,
		Expiry:       time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second),
	}
	return tok, nil
}

// RevokeToken hits Google's revocation endpoint. Either an access or a refresh
// token can be passed; revoking a refresh token also invalidates all access
// tokens minted from it. Returns nil on success and on the "already invalid"
// case (Google returns 400 invalid_token, which is fine — the user wanted it
// gone and it's gone).
func RevokeToken(ctx context.Context, token string) error {
	if token == "" {
		return errors.New("revoke: empty token")
	}
	form := url.Values{}
	form.Set("token", token)
	req, err := http.NewRequestWithContext(ctx, "POST", googleRevokeURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("revoke transport: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusOK {
		return nil
	}
	// Google returns 400 invalid_token for an already-revoked token — treat as
	// success since the desired end state is reached.
	if resp.StatusCode == http.StatusBadRequest && strings.Contains(string(body), "invalid_token") {
		return nil
	}
	return fmt.Errorf("revoke: %s: %s", resp.Status, body)
}

// IsRefreshError reports whether err looks like a permanent OAuth refresh
// failure — invalid_grant from Google means the refresh token has been
// revoked, expired (7-day Testing-mode cap), or the user changed their
// password. Permanent failures should trigger a re-auth DM; transient ones
// (network blips, 5xx) should not.
//
// Pattern-matches both the structured *oauth2.RetrieveError shape (returned by
// the standard oauth2 library when refresh fails inside TokenSource.Token())
// and our own ExchangeRefreshToken error strings.
func IsRefreshError(err error) bool {
	if err == nil {
		return false
	}
	var retrieve *oauth2.RetrieveError
	if errors.As(err, &retrieve) {
		switch retrieve.ErrorCode {
		case "invalid_grant", "invalid_token", "unauthorized_client":
			return true
		}
	}
	msg := err.Error()
	for _, needle := range []string{
		"invalid_grant",
		"invalid_token",
		"Token has been expired or revoked",
		"unauthorized_client",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}
