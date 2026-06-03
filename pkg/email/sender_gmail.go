package email

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/rs/zerolog"
	"golang.org/x/oauth2"
	gmail "google.golang.org/api/gmail/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

// GmailAPISender implements Sender via Gmail's REST API. It uses an
// auto-refreshing oauth2.TokenSource so callers don't have to manage token
// expiry; refreshes happen transparently inside the google API client.
type GmailAPISender struct {
	tokenSource oauth2.TokenSource
	log         *zerolog.Logger
}

// NewGmailAPISender constructs a GmailAPISender. ts must be a refresh-aware
// TokenSource (e.g. one returned by TokenSource in oauth_gmail.go).
func NewGmailAPISender(ts oauth2.TokenSource, log *zerolog.Logger) *GmailAPISender {
	return &GmailAPISender{tokenSource: ts, log: log}
}

// Provider returns the transport identifier "gmail-api".
func (g *GmailAPISender) Provider() string { return "gmail-api" }

// Close is a no-op; the underlying *http.Client is owned by the gmail service
// constructed per Send call.
func (g *GmailAPISender) Close() error { return nil }

// Send POSTs the MIME bytes to Users.Messages.Send and then re-fetches the
// resulting message to read its server-assigned Message-ID header. The caller
// MUST use the returned messageID as the dedup key — Gmail rewrites the
// Message-ID for messages it sends.
//
// If the send succeeds but the post-fetch fails, Send returns ("", nil) with
// a warn-level log; the caller is expected to fall back to the Message-ID it
// wrote into the MIME headers (which means a small risk of double-posting on
// IMAP IDLE pickup).
//
// When threadID is non-empty it is set as gmail.Message.ThreadId so Gmail files
// the reply inside the existing conversation. The ThreadId is only a best-effort
// grouping hint: a stale or cross-mailbox ID (re-auth, the thread moved/deleted
// server-side, an ID learned under a different account) makes Gmail reject the
// whole send with 404 notFound. We MUST NOT let that block delivery — on such a
// rejection we resend once without the ThreadId, trading conversation grouping
// in the sender's mailbox for a message that actually goes out.
func (g *GmailAPISender) Send(ctx context.Context, mimeBytes []byte, from string, to []string, threadID string) (string, error) {
	_ = from // userId="me" implies the authenticated user; from header inside the MIME is what Gmail uses
	if len(to) == 0 {
		return "", errors.New("gmail: no recipients")
	}
	if g.tokenSource == nil {
		return "", errors.New("gmail: nil token source")
	}

	svc, err := gmail.NewService(ctx, option.WithTokenSource(g.tokenSource))
	if err != nil {
		return "", fmt.Errorf("gmail service: %w", err)
	}

	// Gmail API expects base64url-encoded raw RFC 5322. URL-safe encoding
	// without padding is what the API documentation specifies.
	raw := base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(mimeBytes)

	outMsg := &gmail.Message{Raw: raw}
	if threadID != "" {
		outMsg.ThreadId = threadID
	}

	sent, err := svc.Users.Messages.Send("me", outMsg).Context(ctx).Do()
	if err != nil && threadID != "" && isGmailBadThreadErr(err) {
		// The ThreadId didn't resolve in this mailbox. Drop it and retry so the
		// message still sends (it just won't group under the original
		// conversation in the sender's Sent view).
		if g.log != nil {
			g.log.Warn().Err(err).Str("gmail_thread_id", threadID).
				Msg("Gmail rejected ThreadId; resending without thread grouping")
		}
		outMsg.ThreadId = ""
		sent, err = svc.Users.Messages.Send("me", outMsg).Context(ctx).Do()
	}
	if err != nil {
		return "", fmt.Errorf("gmail send: %w", err)
	}

	// sent.Id is the Gmail-internal ID (e.g. 18c8...). To get the RFC 5322
	// Message-ID header that downstream consumers (and our IMAP echo dedup)
	// will see, we re-fetch the message asking only for that header.
	full, err := svc.Users.Messages.
		Get("me", sent.Id).
		Format("metadata").
		MetadataHeaders("Message-ID").
		Context(ctx).
		Do()
	if err != nil {
		if g.log != nil {
			g.log.Warn().Err(err).Str("gmail_id", sent.Id).
				Msg("Gmail send succeeded but failed to fetch server Message-ID; dedup may double-post")
		}
		return "", nil
	}
	if full == nil || full.Payload == nil {
		return "", nil
	}
	for _, h := range full.Payload.Headers {
		if strings.EqualFold(h.Name, "Message-ID") {
			return strings.Trim(h.Value, "<>"), nil
		}
	}
	return "", nil
}

// isGmailBadThreadErr reports whether err is a Gmail API rejection attributable
// to an unusable ThreadId. Gmail returns 404 notFound when the thread doesn't
// exist in the mailbox, and 400 for a malformed / cross-mailbox thread
// reference; we only treat a 400 as thread-related when the message mentions
// "thread" so genuine 400s (bad recipient, oversized message) still surface.
func isGmailBadThreadErr(err error) bool {
	var gerr *googleapi.Error
	if !errors.As(err, &gerr) {
		return false
	}
	if gerr.Code == 404 {
		return true
	}
	return gerr.Code == 400 && strings.Contains(strings.ToLower(gerr.Message), "thread")
}
