package connector

import (
	"context"
	"fmt"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/format"
	"maunium.net/go/mautrix/id"
)

// loginIDFromReceiver converts a receiver string (the form stored on the
// processor's threading scope) back into a networkid.UserLoginID for
// bridgev2 cache lookups.
func loginIDFromReceiver(receiver string) networkid.UserLoginID {
	return networkid.UserLoginID(receiver)
}

// postErrorToRoom posts a user-visible notice into a specific Matrix room.
// title is a short headline; details may include err.Error() (callers should
// sanitize first if it could contain PII/credentials).
func postErrorToRoom(ctx context.Context, bridge *bridgev2.Bridge, roomID id.RoomID, title, details string) error {
	if bridge == nil {
		return fmt.Errorf("postErrorToRoom: nil bridge")
	}
	if roomID == "" {
		return fmt.Errorf("postErrorToRoom: empty roomID")
	}
	intent := bridge.Bot
	if intent == nil {
		return fmt.Errorf("postErrorToRoom: bridge bot intent unavailable")
	}
	md := fmt.Sprintf("⚠️ **%s**", title)
	if details != "" {
		md += "\n\n" + details
	}
	content := format.RenderMarkdown(md, true, false)
	content.MsgType = event.MsgNotice
	if _, err := intent.SendMessage(ctx, roomID, event.EventMessage, &event.Content{Parsed: &content}, nil); err != nil {
		return fmt.Errorf("send notice: %w", err)
	}
	return nil
}

// postErrorToPortal looks up the Matrix room for the portal and forwards to
// postErrorToRoom. No-op (returns nil) if portal has no MXID yet.
func postErrorToPortal(ctx context.Context, bridge *bridgev2.Bridge, portal *bridgev2.Portal, title, details string) error {
	if portal == nil {
		return fmt.Errorf("postErrorToPortal: nil portal")
	}
	if portal.MXID == "" {
		// No room provisioned yet — nothing we can post into.
		return nil
	}
	return postErrorToRoom(ctx, bridge, portal.MXID, title, details)
}

// processorErrorNotifier adapts the email.Processor's ErrorNotifier hook to
// the connector's portal/room machinery. Best-effort: failures to find the
// portal or post the notice are logged and swallowed.
type processorErrorNotifier struct {
	ec *EmailConnector
}

// NotifyProcessingError surfaces a processing error to the user. We don't
// have direct access to the affected portal here — the processor calls this
// before threading finishes — so we fall back to the user's management room.
// TODO: thread the portal through once available so notices land in the
// affected room rather than the management room.
func (n *processorErrorNotifier) NotifyProcessingError(ctx context.Context, receiver, messageID, subject, kind string, err error) {
	if n == nil || n.ec == nil || n.ec.Bridge == nil {
		return
	}
	login := n.ec.Bridge.GetCachedUserLoginByID(loginIDFromReceiver(receiver))
	if login == nil || login.User == nil {
		return
	}
	md := fmt.Sprintf("Failed to process inbound email (%s): %s", kind, err.Error())
	if subject != "" {
		md += fmt.Sprintf("\nSubject: %s", subject)
	}
	if msgErr := sendBridgeNotice(ctx, login.User, md); msgErr != nil {
		n.ec.Bridge.Log.Warn().Err(msgErr).Str("kind", kind).Msg("Failed to deliver processing-error notice")
	}
}
