package connector

import (
	"context"
	"fmt"
	"sync"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/status"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"

	"github.com/Leicas/matrimail/pkg/email"
)

// EmailReauthRequired is the bridge-state error code surfaced when an OAuth
// account's refresh token has been revoked / expired and the user needs to
// run !matrimail login again. Beeper / Element surface this in the bridge
// status UI and the user's management room.
var EmailReauthRequired = status.BridgeStateErrorCode("E-EMAIL-006")

// reauthDebounceWindow caps how often we send a "please re-auth" DM for the
// same account. A short refresh-failure storm (one per IMAP reconnect, one
// per send attempt) shouldn't translate into a flood of DMs — once per hour
// is plenty.
const reauthDebounceWindow = time.Hour

// In-memory debounce: persists across goroutine bursts within the same
// process, and supplements the on-disk last_reauth_notice_at column (which
// catches restarts but not racing concurrent goroutines on a fresh row).
var (
	reauthMemoMu sync.Mutex
	reauthMemo   = map[string]time.Time{}
)

func reauthMemoKey(userMXID, email string) string {
	return userMXID + "|" + email
}

// MarkAccountNeedsReauth flips the account row to AuthTypeOAuthGmailNeedsReauth
// so subsequent connection attempts skip until the user re-logs in. Idempotent.
func (ec *EmailConnector) MarkAccountNeedsReauth(ctx context.Context, userMXID, email string) error {
	if ec == nil || ec.DB == nil {
		return fmt.Errorf("MarkAccountNeedsReauth: connector or DB nil")
	}
	return ec.DB.SetAuthType(ctx, userMXID, email, AuthTypeOAuthGmailNeedsReauth)
}

// NotifyReauthRequired sends a "please re-auth" DM into the user's bridge
// management room, debounced to at most once per hour per account. Best-effort:
// if the bridge framework's notice path is unavailable, falls back to logging
// (the bridge-state report from ReportReauthBridgeState still surfaces the
// problem in the user's client).
//
// login may be nil during early-startup paths where we know the userMXID but
// haven't loaded the UserLogin yet — in that case we skip the DM and rely
// solely on bridge state + log.
func (ec *EmailConnector) NotifyReauthRequired(ctx context.Context, login *bridgev2.UserLogin, userMXID, emailAddr, scopeMode string) {
	if ec == nil {
		return
	}
	now := time.Now()

	// In-memory debounce check.
	key := reauthMemoKey(userMXID, emailAddr)
	reauthMemoMu.Lock()
	if last, ok := reauthMemo[key]; ok && now.Sub(last) < reauthDebounceWindow {
		reauthMemoMu.Unlock()
		return
	}
	reauthMemo[key] = now
	reauthMemoMu.Unlock()

	// On-disk debounce check (catches process restarts).
	if last, err := ec.DB.LastReauthNotifiedAt(ctx, userMXID, emailAddr); err == nil && !last.IsZero() && now.Sub(last) < reauthDebounceWindow {
		return
	}

	msg := buildReauthMessage(emailAddr, scopeMode)
	logger := ec.Bridge.Log.With().Str("component", "reauth").Str("email", emailAddr).Logger()

	if login != nil && login.User != nil {
		if err := sendBridgeNotice(ctx, login.User, msg); err != nil {
			logger.Warn().Err(err).Msg("Failed to send reauth DM; user will only see bridge-state error and next !matrimail status output")
		} else {
			logger.Info().Msg("Sent reauth DM")
		}
	} else {
		logger.Warn().Msg("Reauth required but no UserLogin handle to DM the user; bridge-state error will surface it instead")
	}

	// Persist the notice timestamp so we don't spam the user across restarts.
	if err := ec.DB.MarkReauthNotified(ctx, userMXID, emailAddr, now); err != nil {
		logger.Warn().Err(err).Msg("Failed to persist last_reauth_notice_at")
	}
}

// ReportReauthBridgeState pushes a bridge-state error so Beeper / Element
// render a "needs re-auth" banner on the affected account.
func ReportReauthBridgeState(coord interface {
	ReportSimpleEvent(stage, event string, connected bool, errCode status.BridgeStateErrorCode, info map[string]any)
}, emailAddr string) {
	if coord == nil {
		return
	}
	coord.ReportSimpleEvent("inbox", "reauth_required", false, EmailReauthRequired, map[string]any{
		"email": emailAddr,
	})
}

// HandleRefreshError is the single hook invoked from token-refresh error
// paths (IMAP SetTokenProvider callback, Gmail API sender wrapper). It
// inspects the error, and on a permanent failure flips the account's
// auth_type, fires a DM, and reports the bridge-state error.
//
// Returns true when the error was a permanent refresh failure (caller should
// stop trying to use the account); false for transient errors (caller should
// log + retry).
func (ec *EmailConnector) HandleRefreshError(ctx context.Context, login *bridgev2.UserLogin, userMXID, emailAddr, scopeMode string, err error) bool {
	if !email.IsRefreshError(err) {
		return false
	}
	if mErr := ec.MarkAccountNeedsReauth(ctx, userMXID, emailAddr); mErr != nil {
		ec.Bridge.Log.Warn().Err(mErr).Str("email", emailAddr).Msg("Failed to mark account needs reauth")
	}
	ec.NotifyReauthRequired(ctx, login, userMXID, emailAddr, scopeMode)
	return true
}

// buildReauthMessage is the body of the "please re-auth" DM. Stays
// scope-mode-aware so users in `full` mode see the 7-day-Testing explainer
// without us spamming `modify` users with irrelevant FAQ.
func buildReauthMessage(emailAddr, scopeMode string) string {
	base := fmt.Sprintf(`🔐 **Matrimail needs you to re-authorize Google for %s**

The OAuth refresh token for this account is no longer valid. This usually means:

- You revoked matrimail's access at https://myaccount.google.com/permissions, or
- You changed your Google password / security settings, or`, emailAddr)

	if scopeMode == ScopeModeFull {
		base += `
- The 7-day refresh-token expiry kicked in (your OAuth project is in "Testing"
  publishing status; ` + "`mail.google.com`" + ` is a restricted scope so escaping
  Testing requires Google verification + CASA assessment — switching to
  ` + "`scope_mode: modify`" + ` is usually a better answer).`
	}

	base += `

Run ` + "`!matrimail login`" + ` to reconnect this account. Until then, this
account is paused — the bridge will not retry and will not lose any messages
that arrive while it's paused.`
	return base
}

// sendBridgeNotice sends a markdown-formatted notice into the user's bridge
// management room. Uses the standard bridgev2 management-room mechanism
// (User.GetManagementRoom + intent.SendMessageEvent).
//
// The exact bridgev2 / mautrix API surface for sending an unsolicited message
// from the bridge bot can vary by framework version. This implementation uses
// the conservative `SendMessageEvent` path; if the bridgev2 version this
// repo pins exposes a different name (e.g. `SendMessage`), adjust here. The
// failure mode is non-critical — the bridge-state error from
// ReportReauthBridgeState already surfaces the problem in the user's client.
func sendBridgeNotice(ctx context.Context, user *bridgev2.User, markdown string) error {
	if user == nil {
		return fmt.Errorf("sendBridgeNotice: nil user")
	}
	mgmtRoom, err := user.GetManagementRoom(ctx)
	if err != nil {
		return fmt.Errorf("get management room: %w", err)
	}
	if mgmtRoom == "" {
		return fmt.Errorf("user has no management room")
	}
	intent := user.Bridge.Bot
	if intent == nil {
		return fmt.Errorf("bridge bot intent unavailable")
	}
	content := format.RenderMarkdown(markdown, true, false)
	content.MsgType = event.MsgNotice
	if _, err := intent.SendMessage(ctx, mgmtRoom, event.EventMessage, &event.Content{Parsed: &content}, nil); err != nil {
		return fmt.Errorf("send notice: %w", err)
	}
	return nil
}

