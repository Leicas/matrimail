package email

import (
	"context"
	"fmt"
	"html"
	"regexp"
	"strings"

	gmail "google.golang.org/api/gmail/v1"
)

// FetchGmailSignature returns the HTML signature configured in the user's
// Gmail settings for the given send-as address. Returns empty string (no
// error) when the account has no signature set, or when no send-as entry
// matches.
//
// Picks the matching send-as by case-insensitive address match first, then
// falls back to the IsPrimary entry — Workspace users typically have exactly
// one send-as (their own address) so the fallback is a no-op for them, but
// users with aliases get the right one.
//
// Required scope: gmail.modify (which our login flow already requests) is
// sufficient — gmail.settings.basic / gmail.readonly / mail.google.com all
// also work, but we don't need the dedicated scopes.
func FetchGmailSignature(ctx context.Context, svc *gmail.Service, sendAsEmail string) (string, error) {
	if svc == nil {
		return "", fmt.Errorf("FetchGmailSignature: nil gmail service")
	}
	resp, err := svc.Users.Settings.SendAs.List("me").Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("list send-as: %w", err)
	}
	for _, sa := range resp.SendAs {
		if strings.EqualFold(sa.SendAsEmail, sendAsEmail) {
			return sa.Signature, nil
		}
	}
	for _, sa := range resp.SendAs {
		if sa.IsPrimary {
			return sa.Signature, nil
		}
	}
	return "", nil
}

// GmailSendAs describes one send-as identity (primary or alias) returned by
// users.settings.sendAs.list. The bridge uses the alias list to (a) detect
// inbound DeliveredTo (which alias an incoming mail was addressed to) and
// (b) preserve that alias as the From on outbound replies.
type GmailSendAs struct {
	Email     string
	Name      string // display name from the send-as settings
	IsPrimary bool
	Signature string
}

// FetchGmailSendAsList returns all send-as identities (primary and aliases)
// for the authenticated user. Best-effort at the call site — empty slice on
// any error so a misconfigured / unauthorized account doesn't break inbound.
func FetchGmailSendAsList(ctx context.Context, svc *gmail.Service) ([]GmailSendAs, error) {
	if svc == nil {
		return nil, fmt.Errorf("FetchGmailSendAsList: nil gmail service")
	}
	resp, err := svc.Users.Settings.SendAs.List("me").Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("list send-as: %w", err)
	}
	out := make([]GmailSendAs, 0, len(resp.SendAs))
	for _, sa := range resp.SendAs {
		if sa == nil || sa.SendAsEmail == "" {
			continue
		}
		out = append(out, GmailSendAs{
			Email:     sa.SendAsEmail,
			Name:      sa.DisplayName,
			IsPrimary: sa.IsPrimary,
			Signature: sa.Signature,
		})
	}
	return out, nil
}

// htmlTagRe matches HTML tags so we can strip them when deriving a plain-text
// fallback for the signature. Not a real HTML parser — Gmail signatures are
// usually simple (links, line breaks, the occasional image), and this gets
// 95% of real-world cases right without pulling in golang.org/x/net/html.
var htmlTagRe = regexp.MustCompile(`<[^>]+>`)

// HTMLSignatureToText converts a Gmail HTML signature to a rough plain-text
// approximation suitable for the text/plain alternative of a multipart
// message. Block-level tags become newlines; inline tags are stripped; HTML
// entities are unescaped. For the "Gmail signature with a logo image and a
// hyperlink to my LinkedIn" shape that 99% of users actually have, this is
// indistinguishable from what Gmail itself emits in the text alternative.
//
// Returns empty string if the input is empty or whitespace-only.
func HTMLSignatureToText(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	// Replace block-level tags and <br> with newlines BEFORE stripping all
	// tags, so we keep the line structure.
	br := regexp.MustCompile(`(?i)<br\s*/?>|</p>|</div>|</li>|</tr>`)
	s = br.ReplaceAllString(s, "\n")
	s = htmlTagRe.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	// Collapse runs of 3+ newlines down to 2 (paragraph breaks) — gmail
	// signatures often have nested divs that would otherwise produce big
	// vertical gaps in the text alternative.
	multiNL := regexp.MustCompile(`\n{3,}`)
	s = multiNL.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

// AppendSignature returns the message body with the user's signature appended
// using the RFC 3676 plain-text delimiter ("\n-- \n", note the trailing
// space) for plain text and a horizontal-rule-equivalent separator for HTML.
// Returns the body unchanged if signature is empty.
//
// Plain-text and HTML variants differ:
//   - Plain text: "\n\n-- \n<sig-as-text>"
//   - HTML:       "<br><br>-- <br><sig-as-html>"
//
// The "-- " (hyphen, hyphen, space) sigil is the standard signature delimiter
// since RFC 1849 / RFC 3676 §4.3 and is what email clients use to fold or
// hide signatures in quoted replies.
func AppendSignature(body, signatureHTML string, asHTML bool) string {
	if strings.TrimSpace(signatureHTML) == "" {
		return body
	}
	if asHTML {
		return body + "<br><br>-- <br>" + signatureHTML
	}
	return body + "\n\n-- \n" + HTMLSignatureToText(signatureHTML)
}
