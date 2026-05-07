// Phase C addition: outbound (Matrix -> Email) flow.
//
// This file contains EmailClient.handleMatrixMessageOutbound — the full
// implementation invoked by HandleMatrixMessage in client.go. Lives in its
// own file for review hygiene; nothing else needs to import it.
//
// The flow:
//
//  1. Resolve the EmailThread for this portal via ThreadManager.
//  2. Compute In-Reply-To and References from the reply target / thread state.
//  3. Resolve recipients = thread participants minus self.
//  4. Build OutgoingMessage (Re: prefix, optional HTML body, attachments).
//  5. Download Matrix media (if media msgtype) into an EmailAttachment.
//  6. BuildMIME -> Sender.Send -> SentDedup.Record.
//  7. Best-effort APPEND to Sent (SMTP only — Gmail API auto-files).
//  8. Update thread cache so subsequent replies thread correctly.
//  9. Return MatrixMessageResponse with the network MessageID.
package connector

import (
	"context"
	"errors"
	"fmt"
	netmail "net/mail"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"github.com/Leicas/matrimail/pkg/email"
)

// handleMatrixMessageOutbound is the full outbound implementation. Errors are
// returned to bridgev2 (which will surface them as message-status events to
// the user); successful sends record dedup state and return the network
// MessageID for the framework to persist.
func (ec *EmailClient) handleMatrixMessageOutbound(ctx context.Context, msg *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, error) {
	if ec.Sender == nil {
		return nil, errors.New("matrimail: outbound disabled (no Sender configured for this account)")
	}
	if msg == nil || msg.Content == nil || msg.Portal == nil {
		return nil, errors.New("matrimail: invalid Matrix message (missing content or portal)")
	}

	thread, err := ec.resolveThreadForPortal(msg.Portal.ID)
	if err != nil {
		return nil, err
	}

	inReplyTo, references := computeReplyChain(thread, msg.ReplyTo)
	to, err := resolveRecipients(thread.Participants, ec.Email)
	if err != nil {
		return nil, err
	}

	om := buildOutgoingMessage(ec.Email, thread, msg, inReplyTo, references, to)

	// Pull Matrix media (if any) into an EmailAttachment. Best-effort: a
	// failure here aborts the send because the user clearly intended to
	// attach something.
	if msg.Content.URL != "" || msg.Content.File != nil {
		att, attErr := ec.downloadMediaAsAttachment(ctx, msg.Content)
		if attErr != nil {
			return nil, fmt.Errorf("download attachment: %w", attErr)
		}
		om.Attachments = append(om.Attachments, att)
	}

	mimeBytes, err := om.BuildMIME()
	if err != nil {
		return nil, fmt.Errorf("build mime: %w", err)
	}

	recipientStrs := make([]string, len(to))
	for i, a := range to {
		recipientStrs[i] = a.Address
	}

	serverID, err := ec.Sender.Send(ctx, mimeBytes, ec.Email, recipientStrs)
	if err != nil {
		return nil, fmt.Errorf("send: %w", err)
	}

	// Gmail API may rewrite the Message-ID server-side; if so, prefer that
	// value as the dedup key (it's what the IMAP IDLE echo will carry).
	dedupKey := strings.Trim(om.MessageID, "<>")
	if serverID != "" {
		dedupKey = strings.Trim(serverID, "<>")
	}

	matrixEvtID := ""
	if msg.Event != nil {
		matrixEvtID = string(msg.Event.ID)
	}

	if ec.Main.SentDedup != nil {
		if err := ec.Main.SentDedup.Record(ctx, string(ec.UserLogin.ID), dedupKey, matrixEvtID); err != nil {
			ec.UserLogin.Log.Warn().Err(err).Msg("dedup record failed; outbound may double-post on Sent IDLE echo")
		}
	}

	// Best-effort APPEND to Sent so users see their outbound in Gmail web /
	// iOS Mail without waiting for SMTP->IMAP propagation. Skip for the
	// Gmail API path because the API already filed the message in Sent.
	if ec.Sender.Provider() == "smtp" && ec.IMAPClient != nil {
		if err := ec.IMAPClient.AppendToSentFolder(ctx, mimeBytes); err != nil {
			ec.UserLogin.Log.Warn().Err(err).Msg("append to Sent failed; message will appear after SMTP->IMAP propagation")
		}
	}

	// Update thread cache so subsequent replies in the same room thread
	// against this newly-sent message rather than against the previous tail.
	if ec.Main.ThreadManager != nil {
		thread.References = append(thread.References, dedupKey)
		thread.MessageID = dedupKey
		ec.Main.ThreadManager.CacheForReceiver(string(ec.UserLogin.ID), thread)
	}

	return &bridgev2.MatrixMessageResponse{
		DB: &database.Message{
			ID:        networkid.MessageID("email:" + dedupKey),
			SenderID:  MakeUserID(ec.Email),
			Timestamp: time.Now(),
		},
	}, nil
}

// resolveThreadForPortal looks up the EmailThread that backs this Matrix
// portal. Stripped portal IDs are of the form "thread:<message-id>". If the
// thread isn't in the in-memory cache (bridge restart, never received in this
// room, etc.) we surface a clear error rather than silently dropping the
// message — Phase D will replenish for compose-only portals via metadata.
func (ec *EmailClient) resolveThreadForPortal(portalID networkid.PortalID) (*email.EmailThread, error) {
	threadID := strings.TrimPrefix(string(portalID), "thread:")
	if threadID == "" {
		return nil, errors.New("matrimail: portal has no thread ID")
	}
	if ec.Main.ThreadManager == nil {
		return nil, errors.New("matrimail: ThreadManager not initialized")
	}
	thread := ec.Main.ThreadManager.GetThreadByID(string(ec.UserLogin.ID), threadID)
	if thread == nil {
		return nil, fmt.Errorf("matrimail: thread %s not found in cache (re-receive a message in this room to re-cache, or this is a stale room)", threadID)
	}
	return thread, nil
}

// computeReplyChain returns (in-reply-to, references) headers for an outbound
// reply. Honors an explicit Matrix reply when present; otherwise falls back
// to the thread tail.
func computeReplyChain(thread *email.EmailThread, replyTo *database.Message) (string, []string) {
	references := append([]string(nil), thread.References...)
	if replyTo != nil {
		parentID := strings.TrimPrefix(string(replyTo.ID), "email:")
		references = append(references, parentID)
		return parentID, references
	}
	if thread.MessageID != "" {
		references = append(references, thread.MessageID)
		return thread.MessageID, references
	}
	return "", references
}

// resolveRecipients filters the thread participant list, dropping the
// authenticated user (case-insensitive). Returns an error when the resulting
// list is empty — there's no point sending mail to nobody.
//
// This is reply-all behavior; Phase D's !matrimail compose handler will let
// the user narrow the To/Cc set.
func resolveRecipients(participants []string, self string) ([]netmail.Address, error) {
	selfLower := strings.ToLower(self)
	var to []netmail.Address
	for _, p := range participants {
		if strings.ToLower(p) == selfLower {
			continue
		}
		if a, err := netmail.ParseAddress(p); err == nil {
			to = append(to, *a)
		}
	}
	if len(to) == 0 {
		return nil, errors.New("matrimail: no recipients (thread participants empty after self-exclusion)")
	}
	return to, nil
}

// buildOutgoingMessage assembles the OutgoingMessage from the Matrix event
// content and resolved threading metadata. Re: prefixing matches RFC 5322
// convention (case-insensitive check so we don't double-prefix).
func buildOutgoingMessage(selfEmail string, thread *email.EmailThread, msg *bridgev2.MatrixMessage, inReplyTo string, references []string, to []netmail.Address) *email.OutgoingMessage {
	domain := ""
	if at := strings.LastIndex(selfEmail, "@"); at >= 0 {
		domain = selfEmail[at+1:]
	}
	newMsgID := strings.Trim(email.GenerateMessageID(domain), "<>")

	subject := thread.Subject
	if msg.ReplyTo != nil && !strings.HasPrefix(strings.ToLower(strings.TrimSpace(subject)), "re:") {
		subject = "Re: " + subject
	}

	textBody := msg.Content.Body
	htmlBody := ""
	if msg.Content.Format == event.FormatHTML {
		htmlBody = msg.Content.FormattedBody
	}

	return &email.OutgoingMessage{
		MessageID:  newMsgID,
		From:       netmail.Address{Address: selfEmail},
		To:         to,
		Subject:    subject,
		Date:       time.Now(),
		InReplyTo:  inReplyTo,
		References: references,
		TextBody:   textBody,
		HTMLBody:   htmlBody,
	}
}

// downloadMediaAsAttachment pulls a Matrix media payload (encrypted or not)
// into a single EmailAttachment ready to be appended to an OutgoingMessage.
func (ec *EmailClient) downloadMediaAsAttachment(ctx context.Context, content *event.MessageEventContent) (*email.EmailAttachment, error) {
	if ec.UserLogin == nil || ec.UserLogin.Bridge == nil || ec.UserLogin.Bridge.Bot == nil {
		return nil, errors.New("matrimail: bridge bot intent not available")
	}
	data, err := ec.UserLogin.Bridge.Bot.DownloadMedia(ctx, content.URL, content.File)
	if err != nil {
		return nil, err
	}
	mime := ""
	if content.Info != nil {
		mime = content.Info.MimeType
	}
	if mime == "" {
		mime = "application/octet-stream"
	}
	return &email.EmailAttachment{
		Filename:    content.GetFileName(),
		ContentType: mime,
		Size:        int64(len(data)),
		Data:        data,
		Disposition: "attachment",
	}, nil
}
