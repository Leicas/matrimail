package email

import (
	"strings"
	"testing"
	"time"
)

func TestStripQuotedReply_GmailAttribution(t *testing.T) {
	body := `Sounds good — let's go with option B.

Thanks!

On Mon, May 19, 2026 at 10:30 AM John Doe <john@example.com> wrote:

> Hey, just checking which option you prefer.
> A or B?
>
> Cheers,
> John`
	got := StripQuotedReply(body)
	want := "Sounds good — let's go with option B.\n\nThanks!"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestStripQuotedReply_OutlookDivider(t *testing.T) {
	body := `Confirmed, I'll be there.

-----Original Message-----
From: Jane <jane@example.com>
Sent: Monday, May 19, 2026 9:00 AM
To: me@example.com
Subject: Meeting Friday?

Are you available Friday at 3?`
	got := StripQuotedReply(body)
	want := "Confirmed, I'll be there."
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestStripQuotedReply_OutlookHeaderBlock(t *testing.T) {
	body := `Yes, that works.

From: Jane <jane@example.com>
Sent: Monday, May 19, 2026 9:00 AM
To: me@example.com
Subject: Meeting Friday?

Are you available Friday at 3?`
	got := StripQuotedReply(body)
	want := "Yes, that works."
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestStripQuotedReply_TrailingQuoteOnly(t *testing.T) {
	body := `Approved.

> please confirm the budget for Q3
> — finance team`
	got := StripQuotedReply(body)
	want := "Approved."
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestStripQuotedReply_NoQuotePreserved(t *testing.T) {
	body := `Just a normal email with no quote.

Multiple paragraphs.
--
Sig line should stay`
	got := StripQuotedReply(body)
	if !strings.HasPrefix(got, "Just a normal email") {
		t.Errorf("body unexpectedly mutated: %q", got)
	}
	if !strings.Contains(got, "Sig line should stay") {
		t.Errorf("signature was stripped: %q", got)
	}
}

func TestStripQuotedReply_EmptyAndWhitespace(t *testing.T) {
	if got := StripQuotedReply(""); got != "" {
		t.Errorf("empty input -> %q", got)
	}
	if got := StripQuotedReply("\n\n\n"); got != "" {
		t.Errorf("whitespace-only -> %q", got)
	}
}

func TestFormatGmailQuoteText_Basic(t *testing.T) {
	d := time.Date(2026, 5, 19, 10, 30, 0, 0, time.UTC)
	got := FormatGmailQuoteText(d, "John Doe <john@example.com>", "Hey,\n\nWhat's the plan?\nThanks,\nJohn")
	want := `On Tue, May 19, 2026 at 10:30 AM, John Doe <john@example.com> wrote:
> Hey,
>
> What's the plan?
> Thanks,
> John`
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestFormatGmailQuoteText_PreservesExistingQuoteDepth(t *testing.T) {
	d := time.Date(2026, 5, 19, 10, 30, 0, 0, time.UTC)
	parent := "My reply.\n\n> Previously quoted line"
	got := FormatGmailQuoteText(d, "jane@example.com", parent)
	if !strings.Contains(got, ">> Previously quoted line") {
		t.Errorf("nested quote depth not preserved: %q", got)
	}
}

func TestFormatGmailQuoteHTML_EmptyReturnsEmpty(t *testing.T) {
	got := FormatGmailQuoteHTML(time.Now(), "x@y.z", "")
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestFormatGmailQuoteHTML_WrapsParent(t *testing.T) {
	d := time.Date(2026, 5, 19, 10, 30, 0, 0, time.UTC)
	got := FormatGmailQuoteHTML(d, "John <john@example.com>", "<p>Hello</p>")
	if !strings.Contains(got, `class="gmail_quote gmail_quote_container"`) {
		t.Errorf("missing gmail_quote container: %q", got)
	}
	if !strings.Contains(got, `class="gmail_attr"`) {
		t.Errorf("missing gmail_attr: %q", got)
	}
	if !strings.Contains(got, "<p>Hello</p>") {
		t.Errorf("parent HTML not embedded: %q", got)
	}
	if !strings.Contains(got, "John &lt;john@example.com&gt;") {
		t.Errorf("attribution not HTML-escaped: %q", got)
	}
}

func TestNormalizeReplySubject(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"Foo", "Re: Foo"},
		{"Re: Foo", "Re: Foo"},
		{"RE: Foo", "Re: Foo"},
		{"re: Foo", "Re: Foo"},
		{"Re: Re: Foo", "Re: Foo"},
		{"RE: re: RE: Foo", "Re: Foo"},
		{"Re[2]: Foo", "Re: Foo"},
		{"  Re:  Re: Foo  ", "Re: Foo"},
		{"", "Re: "},
	}
	for _, c := range cases {
		if got := NormalizeReplySubject(c.in); got != c.want {
			t.Errorf("NormalizeReplySubject(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
