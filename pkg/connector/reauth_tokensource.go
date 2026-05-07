package connector

import (
	"context"
	"sync"

	"golang.org/x/oauth2"
	"maunium.net/go/mautrix/bridgev2"
)

// reauthAwareTokenSource wraps an oauth2.TokenSource for the Gmail API
// sender path. Each call to Token() invokes the underlying source; if it
// returns a permanent refresh failure (invalid_grant / Token revoked /
// 7-day Testing-mode expiry), the wrapper triggers the re-auth UX once and
// then keeps surfacing the same error to the caller so the gmail client
// fails fast rather than retrying forever.
//
// Distinct from the IMAP path, which has its own error handling inside
// EmailConnector.createIMAPClient's SetTokenProvider callback.
//
// Persistence of refreshed tokens is also handled here: when the underlying
// source mints a new token, we save it back via SaveOAuthToken so a bridge
// restart doesn't lose the latest access token.
type reauthAwareTokenSource struct {
	inner     oauth2.TokenSource
	connector *EmailConnector
	login     *bridgev2.UserLogin
	userMXID  string
	email     string
	provider  string
	scopeMode string

	mu        sync.Mutex
	persisted *oauth2.Token // most recent token successfully persisted, used to debounce DB writes
}

// newReauthAwareTokenSource wires up a wrapper. inner must be a
// refresh-aware source (e.g. one returned by email.TokenSource) so this
// wrapper only has to worry about side effects, not refresh mechanics.
func newReauthAwareTokenSource(
	inner oauth2.TokenSource,
	connector *EmailConnector,
	login *bridgev2.UserLogin,
	userMXID, emailAddr, provider, scopeMode string,
) *reauthAwareTokenSource {
	return &reauthAwareTokenSource{
		inner:     inner,
		connector: connector,
		login:     login,
		userMXID:  userMXID,
		email:     emailAddr,
		provider:  provider,
		scopeMode: scopeMode,
	}
}

// Token satisfies oauth2.TokenSource. On success it persists the (possibly
// refreshed) token; on permanent refresh failure it fires the re-auth path.
func (r *reauthAwareTokenSource) Token() (*oauth2.Token, error) {
	tok, err := r.inner.Token()
	if err != nil {
		// Long-lived background context here — we want the side-effect
		// goroutine to outlive whatever per-request context the gmail
		// client passed in (which might already be cancelled by the time
		// we report the error back).
		ctx := context.Background()
		_ = r.connector.HandleRefreshError(ctx, r.login, r.userMXID, r.email, r.scopeMode, err)
		return nil, err
	}

	// Persist refreshed tokens. Don't write on every Token() call (the gmail
	// client calls Token frequently); only on actual refresh, detected by
	// access-token change.
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.persisted == nil || r.persisted.AccessToken != tok.AccessToken {
		ctx := context.Background()
		if err := r.connector.DB.SaveOAuthToken(ctx, r.userMXID, r.email, r.provider, tok); err != nil {
			r.connector.Bridge.Log.Warn().
				Err(err).
				Str("email", r.email).
				Msg("Failed to persist refreshed OAuth token from sender path")
		} else {
			r.persisted = tok
		}
	}
	return tok, nil
}
