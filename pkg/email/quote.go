package email

import (
	"fmt"
	netmail "net/mail"
	"regexp"
	"strings"
	"time"
)

// StripQuotedReply removes quoted history from the body of a reply email,
// returning only the "new" portion the sender wrote on top. Mirrors what a
// modern mail client shows above the quote-fold.
//
// The heuristic looks (in order) for the FIRST occurrence of:
//
//  1. A Gmail / Apple-Mail attribution line: `On <date>, <name> <addr> wrote:`
//     (possibly soft-wrapped across two lines).
//  2. An Outlook divider: `-----Original Message-----` or `_____________`.
//  3. An Outlook header block: a line starting with `From:` immediately
//     followed by `Sent:` or `Date:` / `To:` / `Subject:`.
//  4. A run of `>`-quoted lines at the tail of the message.
//
// Everything from that point to EOF is dropped. If none of the heuristics
// match, the original text is returned unchanged — better to show too much
// than to silently truncate the user's message.
//
// The result is right-trimmed (trailing blank lines removed) but otherwise
// preserves the sender's formatting. A signature delimiter (`-- ` on its own
// line) is NOT stripped — signatures belong to the new message.
func StripQuotedReply(text string) string {
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	cut := len(lines)

	for i := 0; i < len(lines); i++ {
		l := strings.TrimRight(lines[i], " \t\r")
		ll := strings.TrimSpace(l)

		// (1) Gmail / Apple Mail: `On ... wrote:`
		if gmailAttrRE.MatchString(ll) {
			cut = i
			break
		}
		// Multi-line variant: `On <date>` ... `<addr> wrote:` split across 2-3 lines.
		if strings.HasPrefix(ll, "On ") && i+1 < len(lines) {
			joined := ll
			for j := 1; j <= 2 && i+j < len(lines); j++ {
				joined += " " + strings.TrimSpace(lines[i+j])
				if gmailAttrRE.MatchString(joined) {
					cut = i
					break
				}
			}
			if cut == i {
				break
			}
		}

		// (2) Outlook dividers
		if ll == "-----Original Message-----" ||
			ll == "________________________________" ||
			outlookDividerRE.MatchString(ll) {
			cut = i
			break
		}

		// (3) Outlook header block: `From: ...` followed by `Sent:`/`Date:`/`To:`/`Subject:`
		if strings.HasPrefix(ll, "From: ") && i+1 < len(lines) {
			next := strings.TrimSpace(lines[i+1])
			if strings.HasPrefix(next, "Sent: ") ||
				strings.HasPrefix(next, "Date: ") ||
				strings.HasPrefix(next, "To: ") ||
				strings.HasPrefix(next, "Subject: ") {
				cut = i
				break
			}
		}
	}

	// (4) Trailing run of `>`-quoted lines (only if nothing else matched).
	if cut == len(lines) {
		j := len(lines) - 1
		// Skip trailing blanks.
		for j >= 0 && strings.TrimSpace(lines[j]) == "" {
			j--
		}
		// Walk backward while we see quoted lines (or blanks between quote blocks).
		end := j + 1
		sawQuote := false
		for j >= 0 {
			trimmed := strings.TrimSpace(lines[j])
			if trimmed == "" {
				j--
				continue
			}
			if strings.HasPrefix(trimmed, ">") {
				sawQuote = true
				j--
				continue
			}
			break
		}
		if sawQuote {
			cut = j + 1
			_ = end
		}
	}

	out := strings.Join(lines[:cut], "\n")
	// Right-trim trailing blank lines and whitespace.
	out = strings.TrimRight(out, " \t\r\n")
	return out
}

// gmailAttrRE matches Gmail / Apple Mail / Thunderbird attribution lines such as:
//
//	On Mon, May 19, 2026 at 10:30 AM John Doe <john@example.com> wrote:
//	On 2026-05-19 10:30, John Doe wrote:
//	On May 19, 2026, at 10:30, John Doe <john@example.com> wrote:
//
// We accept anything between `On ` and ` wrote:` (case-sensitive for the
// keywords; the date content varies wildly by locale).
var gmailAttrRE = regexp.MustCompile(`^On .{3,300}\bwrote:\s*$`)

// outlookDividerRE matches the long underscore divider Outlook injects above
// quoted blocks (12+ underscores on their own line).
var outlookDividerRE = regexp.MustCompile(`^_{12,}$`)

// FormatGmailQuoteText returns a Gmail-style plain-text quote block to be
// appended to a reply body. Layout:
//
//	On <Date>, <Sender> wrote:
//	> <line 1>
//	> <line 2>
//	> ...
//
// The Date is formatted as `Mon, Jan 2, 2006 at 3:04 PM` (Gmail's canonical
// English form). When parentFrom parses as a "Name <addr>" pair the display
// name is used; otherwise the raw From is preserved.
//
// parentText should be the full text body of the parent message (already
// containing its own quote chain if any) — Gmail only quotes one level deep
// at the producer side; the chain accumulates through repeated replies.
func FormatGmailQuoteText(parentDate time.Time, parentFrom, parentText string) string {
	attr := buildAttributionLine(parentDate, parentFrom)
	if parentText == "" {
		return attr + "\n"
	}
	// Prefix every line with "> " (or ">" for empty lines, matching Gmail).
	var b strings.Builder
	b.Grow(len(parentText) + 64)
	b.WriteString(attr)
	b.WriteString("\n")
	for _, line := range strings.Split(parentText, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			b.WriteString(">\n")
		} else if strings.HasPrefix(line, ">") {
			// Already quoted — increment quote depth, no space between markers.
			b.WriteString(">")
			b.WriteString(line)
			b.WriteString("\n")
		} else {
			b.WriteString("> ")
			b.WriteString(line)
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// FormatGmailQuoteHTML returns a Gmail-style HTML quote block. It mirrors the
// exact DOM Gmail emits so threaded clients (Gmail, Apple Mail, Outlook
// modern) collapse it into a "show trimmed content" fold.
//
//	<div class="gmail_quote gmail_quote_container">
//	  <div dir="ltr" class="gmail_attr">On ..., ... wrote:<br></div>
//	  <blockquote class="gmail_quote" style="...">
//	    <parentHTML>
//	  </blockquote>
//	</div>
//
// If parentHTML is empty, no quote block is produced and the caller should
// fall back to omitting the HTML alternative entirely (a text-only quote is
// fine on its own).
func FormatGmailQuoteHTML(parentDate time.Time, parentFrom, parentHTML string) string {
	if parentHTML == "" {
		return ""
	}
	attr := htmlEscape(buildAttributionLine(parentDate, parentFrom))
	return `<div class="gmail_quote gmail_quote_container"><div dir="ltr" class="gmail_attr">` +
		attr + `<br></div><blockquote class="gmail_quote" style="margin:0px 0px 0px 0.8ex;border-left:1px solid rgb(204,204,204);padding-left:1ex">` +
		parentHTML +
		`</blockquote></div>`
}

// buildAttributionLine produces the "On <date>, <sender> wrote:" header used
// by both text and HTML quote builders.
func buildAttributionLine(parentDate time.Time, parentFrom string) string {
	when := parentDate
	if when.IsZero() {
		when = time.Now()
	}
	// Gmail's English format: "Mon, Jan 2, 2006 at 3:04 PM"
	dateStr := when.Format("Mon, Jan 2, 2006 at 3:04 PM")

	sender := strings.TrimSpace(parentFrom)
	if addr, err := netmail.ParseAddress(sender); err == nil {
		name := strings.TrimSpace(addr.Name)
		if name != "" {
			sender = fmt.Sprintf("%s <%s>", name, addr.Address)
		} else {
			sender = addr.Address
		}
	}
	if sender == "" {
		sender = "Unknown"
	}
	return fmt.Sprintf("On %s, %s wrote:", dateStr, sender)
}

// htmlEscape escapes the four HTML metacharacters. We avoid pulling in
// html/template just for this — the attribution line is plain text.
func htmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
	)
	return r.Replace(s)
}

// NormalizeReplySubject collapses a chain of "Re:" / "RE:" / "re:" prefixes
// into a single canonical "Re: " and trims surrounding whitespace. Matches
// Gmail's web-client behavior, which never emits "Re: Re: Foo".
//
// If the subject has no existing Re: prefix, one is added (caller is
// responsible for choosing whether to call this — first sends on a new
// thread should NOT pass through here).
func NormalizeReplySubject(subject string) string {
	s := strings.TrimSpace(subject)
	// Strip any leading Re:/RE:/re: (with optional "[N]") prefixes repeatedly.
	for {
		lower := strings.ToLower(s)
		switch {
		case strings.HasPrefix(lower, "re:"):
			s = strings.TrimSpace(s[3:])
		case strings.HasPrefix(lower, "re["):
			if idx := strings.Index(lower, "]:"); idx > 0 {
				s = strings.TrimSpace(s[idx+2:])
			} else {
				return "Re: " + s
			}
		default:
			return "Re: " + s
		}
	}
}
