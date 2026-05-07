package email

import (
	"context"

	"golang.org/x/oauth2"
)

// GmailOAuthConfig holds the user-provided Google OAuth client credentials.
// User creates these in Google Cloud Console as a "Desktop app" OAuth 2.0
// Client and pastes them into config.yaml under gmail_oauth:.
type GmailOAuthConfig struct {
	ClientID     string
	ClientSecret string
}

// Endpoints for Google's OAuth 2.0 Authorization Code flow. Made package-level
// vars so tests can swap them out for an httptest.Server. The auth URL is
// where we send the user's browser; the token URL handles code exchange and
// refresh; the revoke URL severs a refresh token at the user's request.
var (
	googleAuthURL   = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenURL  = "https://oauth2.googleapis.com/token"
	googleRevokeURL = "https://oauth2.googleapis.com/revoke"
)

// TokenSource returns an auto-refreshing oauth2.TokenSource for the given
// saved token. The standard oauth2 library calls the token URL with the stored
// refresh token whenever the access token is within ~10s of expiry.
//
// Refresh failures bubble up as *oauth2.RetrieveError, which IsRefreshError in
// oauth_authcode.go can pattern-match against to decide whether to trigger a
// re-auth DM.
func TokenSource(ctx context.Context, cfg GmailOAuthConfig, tok *oauth2.Token) oauth2.TokenSource {
	oauthCfg := &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		Endpoint: oauth2.Endpoint{
			AuthURL:  googleAuthURL,
			TokenURL: googleTokenURL,
		},
		// Scopes are recorded inside `tok.RefreshToken` server-side; we don't
		// need to re-pass them here, and re-passing the wrong set would only
		// confuse Google's logging dashboards.
	}
	return oauthCfg.TokenSource(ctx, tok)
}
