package email

import (
	"context"
	"testing"
)

func TestSMTPInfoForEmail(t *testing.T) {
	cases := []struct {
		name     string
		email    string
		wantHost string
		wantPort int
	}{
		{"gmail.com → smtp.gmail.com", "alice@gmail.com", "smtp.gmail.com", 587},
		{"googlemail.com (UK alias) → smtp.gmail.com", "alice@googlemail.com", "smtp.gmail.com", 587},
		{"yahoo.com → smtp.mail.yahoo.com", "bob@yahoo.com", "smtp.mail.yahoo.com", 587},
		{"hotmail.com → smtp.office365.com", "carol@hotmail.com", "smtp.office365.com", 587},
		{"outlook.com → smtp.office365.com", "dan@outlook.com", "smtp.office365.com", 587},
		{"icloud.com → smtp.mail.me.com", "eve@icloud.com", "smtp.mail.me.com", 587},
		{"unknown domain falls back to smtp.<domain>", "user@example.org", "smtp.example.org", 587},
		{"case insensitive", "MIXED@Gmail.COM", "smtp.gmail.com", 587},
		{"trims whitespace", "  trim@gmail.com  ", "smtp.gmail.com", 587},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := SMTPInfoForEmail(tc.email)
			if got.Host != tc.wantHost {
				t.Errorf("Host: got %q, want %q", got.Host, tc.wantHost)
			}
			if got.Port != tc.wantPort {
				t.Errorf("Port: got %d, want %d", got.Port, tc.wantPort)
			}
			if !got.StartTLS {
				t.Errorf("StartTLS: got false, want true")
			}
		})
	}

	// Malformed addresses → zero value. Note: an empty local-part like
	// "@example.com" is still treated as having a usable domain so the helper
	// returns smtp.example.com — that's acceptable for a server-side username
	// validation step elsewhere; not asserted here.
	for _, malformed := range []string{"", "no-at-sign", "user@"} {
		t.Run("malformed/"+malformed, func(t *testing.T) {
			got := SMTPInfoForEmail(malformed)
			if got.Host != "" || got.Port != 0 {
				t.Errorf("malformed input %q should yield zero value, got %+v", malformed, got)
			}
		})
	}
}

func TestIsGmailDomain(t *testing.T) {
	cases := map[string]bool{
		"alice@gmail.com":      true,
		"alice@googlemail.com": true,
		"alice@GMail.COM":      true, // case insensitive
		"alice@workspace.org":  false,
		"alice@yahoo.com":      false,
		"":                     false,
		"no-at-sign":           false,
	}
	for in, want := range cases {
		if got := IsGmailDomain(in); got != want {
			t.Errorf("IsGmailDomain(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestPickSender_GmailWithOAuth(t *testing.T) {
	// A Gmail account with an OAuth token source must yield a GmailAPISender.
	acct := SenderAccount{
		Email:            "alice@gmail.com",
		IsGmail:          true,
		OAuthTokenSource: stubTokenSource{},
	}
	s, err := PickSender(context.Background(), acct, nil)
	if err != nil {
		t.Fatalf("PickSender returned error: %v", err)
	}
	if s.Provider() != "gmail-api" {
		t.Errorf("Provider = %q, want gmail-api", s.Provider())
	}
}

func TestPickSender_GmailWithoutOAuthFallsBackToSMTP(t *testing.T) {
	// Gmail address but no OAuth token (legacy app-password path) → SMTP.
	acct := SenderAccount{
		Email:       "alice@gmail.com",
		IsGmail:     true,
		AppPassword: "abcd efgh ijkl mnop",
	}
	s, err := PickSender(context.Background(), acct, nil)
	if err != nil {
		t.Fatalf("PickSender returned error: %v", err)
	}
	if s.Provider() != "smtp" {
		t.Errorf("Provider = %q, want smtp", s.Provider())
	}
	smtpS, ok := s.(*SMTPSender)
	if !ok {
		t.Fatalf("expected *SMTPSender, got %T", s)
	}
	if smtpS.cfg.Host != "smtp.gmail.com" || smtpS.cfg.Port != 587 || !smtpS.cfg.StartTLS {
		t.Errorf("unexpected SMTP config: %+v", smtpS.cfg)
	}
	if smtpS.cfg.UseXOAUTH2 {
		t.Errorf("UseXOAUTH2 should be false for app-password path")
	}
}

func TestPickSender_NonGmailGoesToSMTP(t *testing.T) {
	acct := SenderAccount{
		Email:       "bob@example.org",
		IsGmail:     false,
		AppPassword: "pw",
	}
	s, err := PickSender(context.Background(), acct, nil)
	if err != nil {
		t.Fatalf("PickSender returned error: %v", err)
	}
	if s.Provider() != "smtp" {
		t.Errorf("Provider = %q, want smtp", s.Provider())
	}
}
