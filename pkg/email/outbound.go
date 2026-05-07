// Package email — Phase B addition: outbound MIME builder.
//
// OutgoingMessage + BuildMIME() compose RFC 5322 messages with proper threading
// headers (Message-ID, In-Reply-To, References) and multipart/mixed bodies for
// attachments. The output is fed to a Sender (SMTP or Gmail API).
//
// Note on package collision: the standard library's net/mail and the
// emersion/go-message/mail packages both export an Address type. We alias them
// as netmail and gomail respectively to keep callers ergonomic.
package email

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	netmail "net/mail"
	"strings"
	"time"

	gomail "github.com/emersion/go-message/mail"
)

// OutgoingMessage is the high-level shape of an email the bridge wants to
// send. The caller fills it from a Matrix m.text/m.image event plus the thread
// context from the portal.
//
// MessageID is the raw RFC 5322 ID (no surrounding angle brackets). Generate
// it with GenerateMessageID; SMTP servers use it verbatim, Gmail's API may
// rewrite it (the GmailAPISender returns the server-assigned value, which
// callers should prefer as the dedup key).
//
// InReplyTo and References likewise hold raw IDs without angle brackets;
// BuildMIME wraps them on emit.
type OutgoingMessage struct {
	MessageID  string             // raw, no <>; the From-side ID we GENERATE
	From       netmail.Address    // sender (display name + addr-spec)
	To         []netmail.Address  // primary recipients
	Cc         []netmail.Address  // carbon-copy
	Bcc        []netmail.Address  // blind carbon-copy (NOT emitted as a header)
	Subject    string             // plain Subject text
	Date       time.Time          // Date header (zero value -> now())
	InReplyTo  string             // raw, no <>; threading parent
	References []string           // raw IDs, in order; threading chain
	TextBody   string             // plain-text alternative (always emitted)
	HTMLBody   string             // optional HTML alternative
	Attachments []*EmailAttachment // see pkg/email/threading.go
}

// BuildMIME serializes the message to RFC 5322 bytes. The returned bytes are
// suitable as input to either an SMTP DATA command or Gmail's
// users.messages.send (after base64url encoding).
//
// The structure is always:
//
//	multipart/mixed
//	├── multipart/alternative
//	│   ├── text/plain  (TextBody)
//	│   └── text/html   (HTMLBody, if non-empty)
//	└── attachment*     (one part per Attachments entry)
//
// Bcc is intentionally NOT written to the headers — the SMTP envelope handles
// blind copies; Gmail API does the same on its side.
func (o *OutgoingMessage) BuildMIME() ([]byte, error) {
	if o == nil {
		return nil, fmt.Errorf("BuildMIME: nil message")
	}

	var buf bytes.Buffer
	var h gomail.Header
	date := o.Date
	if date.IsZero() {
		date = time.Now()
	}
	h.SetDate(date)
	h.SetSubject(o.Subject)
	h.SetAddressList("From", []*netmail.Address{cloneAddr(o.From)})
	if len(o.To) > 0 {
		h.SetAddressList("To", toAddrPtrs(o.To))
	}
	if len(o.Cc) > 0 {
		h.SetAddressList("Cc", toAddrPtrs(o.Cc))
	}
	// Message-ID: emit even when empty — the threading machinery is unsound
	// without one. SetMessageID expects the raw form (no brackets).
	if mid := strings.Trim(o.MessageID, "<>"); mid != "" {
		h.SetMessageID(mid)
	}
	if o.InReplyTo != "" {
		h.Set("In-Reply-To", "<"+strings.Trim(o.InReplyTo, "<>")+">")
	}
	if len(o.References) > 0 {
		refs := make([]string, 0, len(o.References))
		for _, r := range o.References {
			r = strings.Trim(r, "<>")
			if r != "" {
				refs = append(refs, "<"+r+">")
			}
		}
		if len(refs) > 0 {
			h.Set("References", strings.Join(refs, " "))
		}
	}
	h.Set("X-Mailer", "matrimail/0.1 (bridgev2)")
	// MIME-Version is set by go-message itself when CreateWriter runs.

	w, err := gomail.CreateWriter(&buf, h)
	if err != nil {
		return nil, fmt.Errorf("create mail writer: %w", err)
	}

	// Inline (multipart/alternative) wrapper for the bodies.
	inline, err := w.CreateInline()
	if err != nil {
		return nil, fmt.Errorf("create inline: %w", err)
	}

	// text/plain — always present, even if empty, so receivers without HTML
	// support still see something deterministic.
	{
		var ih gomail.InlineHeader
		ih.Set("Content-Type", "text/plain; charset=utf-8")
		pw, err := inline.CreatePart(ih)
		if err != nil {
			return nil, fmt.Errorf("create text part: %w", err)
		}
		if _, err := io.Copy(pw, strings.NewReader(o.TextBody)); err != nil {
			_ = pw.Close()
			return nil, fmt.Errorf("write text body: %w", err)
		}
		if err := pw.Close(); err != nil {
			return nil, fmt.Errorf("close text part: %w", err)
		}
	}

	if o.HTMLBody != "" {
		var ih gomail.InlineHeader
		ih.Set("Content-Type", "text/html; charset=utf-8")
		pw, err := inline.CreatePart(ih)
		if err != nil {
			return nil, fmt.Errorf("create html part: %w", err)
		}
		if _, err := io.Copy(pw, strings.NewReader(o.HTMLBody)); err != nil {
			_ = pw.Close()
			return nil, fmt.Errorf("write html body: %w", err)
		}
		if err := pw.Close(); err != nil {
			return nil, fmt.Errorf("close html part: %w", err)
		}
	}

	if err := inline.Close(); err != nil {
		return nil, fmt.Errorf("close inline: %w", err)
	}

	for _, a := range o.Attachments {
		if a == nil {
			continue
		}
		var ah gomail.AttachmentHeader
		ct := a.ContentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		ah.Set("Content-Type", ct)
		if a.Filename != "" {
			ah.SetFilename(a.Filename)
		}
		// Inline disposition for cid-referenced parts; the body's Content-ID
		// link in HTML uses the same id.
		if strings.EqualFold(a.Disposition, "inline") && a.ContentID != "" {
			ah.Set("Content-ID", "<"+strings.Trim(a.ContentID, "<>")+">")
			ah.Set("Content-Disposition", "inline")
		}
		aw, err := w.CreateAttachment(ah)
		if err != nil {
			return nil, fmt.Errorf("create attachment %q: %w", a.Filename, err)
		}
		if _, err := aw.Write(a.Data); err != nil {
			_ = aw.Close()
			return nil, fmt.Errorf("write attachment %q: %w", a.Filename, err)
		}
		if err := aw.Close(); err != nil {
			return nil, fmt.Errorf("close attachment %q: %w", a.Filename, err)
		}
	}

	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("close mail writer: %w", err)
	}
	return buf.Bytes(), nil
}

// cloneAddr makes a heap-allocated copy of a by-value Address so the gomail
// header API (which insists on pointers) doesn't keep a reference to the
// caller's stack slot.
func cloneAddr(a netmail.Address) *netmail.Address {
	cp := a
	return &cp
}

// toAddrPtrs converts a []netmail.Address (by-value, ergonomic to construct)
// into the []*netmail.Address form go-message wants.
func toAddrPtrs(addrs []netmail.Address) []*netmail.Address {
	out := make([]*netmail.Address, len(addrs))
	for i := range addrs {
		v := addrs[i]
		out[i] = &v
	}
	return out
}

// GenerateMessageID returns a fresh RFC 5322 Message-ID wrapped in angle
// brackets. The caller MUST trim the brackets before assigning to
// OutgoingMessage.MessageID — BuildMIME re-adds them when emitting headers,
// and re-trims on input. This double-bracket dance is intentional: callers
// often store the raw ID for dedup and the bracketed form is only for the
// wire.
//
// domain should be the right-hand side of the From address. Falls back to
// "matrimail.local" when empty so we never emit a header with @@ in it.
func GenerateMessageID(domain string) string {
	if domain == "" {
		domain = "matrimail.local"
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure on a real OS is essentially OOM territory; emit a
		// deterministic-but-unique-by-time fallback so the caller doesn't crash.
		return fmt.Sprintf("<%d.fallback@%s>", time.Now().UnixNano(), domain)
	}
	return fmt.Sprintf("<%d.%s@%s>", time.Now().UnixNano(), hex.EncodeToString(b[:]), domain)
}
