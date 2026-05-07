package email

import (
	"bytes"
	netmail "net/mail"
	"strings"
	"testing"
	"time"
)

func TestBuildMIME_ThreadingHeaders(t *testing.T) {
	msg := &OutgoingMessage{
		MessageID: "abc.123@example.com",
		From:      netmail.Address{Name: "Alice", Address: "alice@example.com"},
		To:        []netmail.Address{{Address: "bob@example.com"}},
		Subject:   "Re: hello",
		Date:      time.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC),
		InReplyTo: "<orig.1@example.com>",
		References: []string{
			"orig.1@example.com",
			"<orig.2@example.com>",
		},
		TextBody: "hi bob\n",
	}
	out, err := msg.BuildMIME()
	if err != nil {
		t.Fatalf("BuildMIME: %v", err)
	}

	headers := topHeader(t, out)

	if got := headers.Get("In-Reply-To"); got != "<orig.1@example.com>" {
		t.Errorf("In-Reply-To = %q; want <orig.1@example.com>", got)
	}
	refs := headers.Get("References")
	if !strings.Contains(refs, "<orig.1@example.com>") || !strings.Contains(refs, "<orig.2@example.com>") {
		t.Errorf("References missing IDs: %q", refs)
	}
	if got := headers.Get("Message-Id"); got == "" || !strings.Contains(got, "abc.123@example.com") {
		t.Errorf("Message-Id missing: %q", got)
	}
	if got := headers.Get("From"); !strings.Contains(got, "alice@example.com") {
		t.Errorf("From missing: %q", got)
	}
	if got := headers.Get("Subject"); got != "Re: hello" {
		t.Errorf("Subject = %q", got)
	}
	if got := headers.Get("X-Mailer"); !strings.Contains(got, "matrimail") {
		t.Errorf("X-Mailer missing: %q", got)
	}
}

func TestBuildMIME_AttachmentMultipartMixed(t *testing.T) {
	msg := &OutgoingMessage{
		MessageID: "att.1@example.com",
		From:      netmail.Address{Address: "alice@example.com"},
		To:        []netmail.Address{{Address: "bob@example.com"}},
		Subject:   "with attachment",
		TextBody:  "body",
		Attachments: []*EmailAttachment{
			{
				Filename:    "hello.txt",
				ContentType: "text/plain",
				Data:        []byte("ATTACHMENT-PAYLOAD"),
			},
		},
	}
	out, err := msg.BuildMIME()
	if err != nil {
		t.Fatalf("BuildMIME: %v", err)
	}

	headers := topHeader(t, out)
	ct := headers.Get("Content-Type")
	if !strings.HasPrefix(strings.ToLower(ct), "multipart/mixed") {
		t.Errorf("top Content-Type = %q; want multipart/mixed", ct)
	}
	if !strings.Contains(ct, "boundary=") {
		t.Errorf("top Content-Type lacks boundary: %q", ct)
	}
	body := string(out)
	if !strings.Contains(body, "hello.txt") {
		t.Errorf("attachment filename not present in body")
	}
}

func TestGenerateMessageID_Bracketed(t *testing.T) {
	id := GenerateMessageID("example.com")
	if !strings.HasPrefix(id, "<") || !strings.HasSuffix(id, ">") {
		t.Errorf("not bracketed: %q", id)
	}
	if !strings.Contains(id, "@example.com") {
		t.Errorf("missing domain: %q", id)
	}
	id2 := GenerateMessageID("example.com")
	if id == id2 {
		t.Errorf("two consecutive IDs equal: %q", id)
	}
	// Empty domain should still produce a valid form.
	idEmpty := GenerateMessageID("")
	if !strings.Contains(idEmpty, "@matrimail.local>") {
		t.Errorf("empty domain fallback wrong: %q", idEmpty)
	}
}

// topHeader parses just the header section of an RFC 5322 message and returns
// it. We avoid fully parsing the body since multipart parsing requires a
// boundary read we don't need here.
func topHeader(t *testing.T, raw []byte) netmail.Header {
	t.Helper()
	m, err := netmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("ReadMessage: %v\n--- raw ---\n%s", err, raw)
	}
	return m.Header
}
