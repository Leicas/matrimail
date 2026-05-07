package connector

import (
	"testing"

	"maunium.net/go/mautrix/bridgev2/networkid"
)

func TestParseComposeArgs_Basic(t *testing.T) {
	c, err := parseComposeArgs(`to:alice@example.com`)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if c.To != "alice@example.com" {
		t.Errorf("To = %q", c.To)
	}
	if len(c.Cc) != 0 {
		t.Errorf("Cc = %+v", c.Cc)
	}
	if c.Subject != "" {
		t.Errorf("Subject = %q", c.Subject)
	}
}

func TestParseComposeArgs_QuotedSubject(t *testing.T) {
	c, err := parseComposeArgs(`to:a@b.com subject:"Hello world"`)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if c.Subject != "Hello world" {
		t.Errorf("Subject = %q", c.Subject)
	}
}

func TestParseComposeArgs_MultipleCc(t *testing.T) {
	c, err := parseComposeArgs(`to:a@b.com cc:c@d.com cc:e@f.com subject:"x"`)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if len(c.Cc) != 2 || c.Cc[0] != "c@d.com" || c.Cc[1] != "e@f.com" {
		t.Errorf("Cc = %+v", c.Cc)
	}
}

func TestParseComposeArgs_MissingTo(t *testing.T) {
	if _, err := parseComposeArgs(`subject:"x"`); err == nil {
		t.Fatal("expected error when to: missing")
	}
}

func TestParseComposeArgs_BadAddress(t *testing.T) {
	if _, err := parseComposeArgs(`to:notanemail`); err == nil {
		t.Fatal("expected error on malformed to:")
	}
	if _, err := parseComposeArgs(`to:a@b.com cc:bogus`); err == nil {
		t.Fatal("expected error on malformed cc:")
	}
}

func TestValidateUserID(t *testing.T) {
	ec := &EmailConnector{}
	cases := []struct {
		id   string
		want bool
	}{
		{"email:alice@example.com", true},
		{"email:Bob.Sender+tag@sub.example.co.uk", true},
		{"alice@example.com", false}, // missing prefix
		{"email:not an email", false},
		{"email:", false},
	}
	for _, tc := range cases {
		got := ec.ValidateUserID(networkid.UserID(tc.id))
		if got != tc.want {
			t.Errorf("ValidateUserID(%q) = %v, want %v", tc.id, got, tc.want)
		}
	}
}

func TestDeriveSubjectFromBody(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "(no subject)"},
		{"   \n\t", "(no subject)"},
		{"Hello", "Hello"},
		{"first line\nsecond line", "first line"},
		// 100 'a's gets truncated to maxLen=78 'a's.
		{
			in:   "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			want: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
	}
	for _, tc := range cases {
		got := deriveSubjectFromBody(tc.in)
		if got != tc.want {
			t.Errorf("deriveSubjectFromBody(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
