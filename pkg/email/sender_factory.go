package email

import (
	"context"
	"strings"

	"github.com/rs/zerolog"
	"golang.org/x/oauth2"
)

// SMTPProviderInfo describes an SMTP submission endpoint for a given email
// provider domain.
type SMTPProviderInfo struct {
	Host     string
	Port     int
	StartTLS bool
}

// smtpProviders maps consumer domains to their well-known SMTP submission
// endpoints. All entries use STARTTLS on 587 — the modern submission profile
// (RFC 6409 / RFC 8314). Domains not in the table fall back to smtp.<domain>:587.
var smtpProviders = map[string]SMTPProviderInfo{
	"gmail.com":      {"smtp.gmail.com", 587, true},
	"googlemail.com": {"smtp.gmail.com", 587, true},
	"yahoo.com":      {"smtp.mail.yahoo.com", 587, true},
	"yahoo.co.uk":    {"smtp.mail.yahoo.com", 587, true},
	"yahoo.fr":       {"smtp.mail.yahoo.com", 587, true},
	"yahoo.de":       {"smtp.mail.yahoo.com", 587, true},
	"outlook.com":    {"smtp.office365.com", 587, true},
	"hotmail.com":    {"smtp.office365.com", 587, true},
	"live.com":       {"smtp.office365.com", 587, true},
	"msn.com":        {"smtp.office365.com", 587, true},
	"office365.com":  {"smtp.office365.com", 587, true},
	"icloud.com":     {"smtp.mail.me.com", 587, true},
	"me.com":         {"smtp.mail.me.com", 587, true},
	"mac.com":        {"smtp.mail.me.com", 587, true},
	"fastmail.com":   {"smtp.fastmail.com", 587, true},
}

// SMTPInfoForEmail returns the SMTP submission endpoint for the given email.
// Falls back to smtp.<domain>:587 STARTTLS for unknown providers. Returns the
// zero value SMTPProviderInfo for malformed addresses.
func SMTPInfoForEmail(email string) SMTPProviderInfo {
	parts := strings.SplitN(strings.ToLower(strings.TrimSpace(email)), "@", 2)
	if len(parts) != 2 || parts[1] == "" {
		return SMTPProviderInfo{}
	}
	if p, ok := smtpProviders[parts[1]]; ok {
		return p
	}
	return SMTPProviderInfo{Host: "smtp." + parts[1], Port: 587, StartTLS: true}
}

// IsGmailDomain reports whether the address is a regular Gmail consumer
// account. Workspace custom domains aren't auto-detectable from the address —
// when the user connects via OAuth, the OAuth-token presence is what tells us
// the account speaks the Gmail API. This helper only fast-paths the well-known
// consumer Gmail domains; the actual Sender selection in PickSender trusts the
// per-account OAuth-token presence and the IsGmail flag.
func IsGmailDomain(emailAddr string) bool {
	parts := strings.SplitN(strings.ToLower(strings.TrimSpace(emailAddr)), "@", 2)
	if len(parts) != 2 {
		return false
	}
	return parts[1] == "gmail.com" || parts[1] == "googlemail.com"
}

// SenderAccount is the minimal shape passed to PickSender. The connector
// populates it from EmailAccount + decrypted OAuth tokens at sender-construction
// time.
type SenderAccount struct {
	Email            string             // user's primary email address (used as SMTP username and SASL identity)
	IsGmail          bool               // true for gmail.com / googlemail.com / Workspace accounts authorized via OAuth
	AppPassword      string             // SMTP path: app password or normal password used with PLAIN auth
	OAuthTokenSource oauth2.TokenSource // Gmail API path; nil when the account is SMTP-only
}

// PickSender decides which Sender impl to use for a given account.
//
//   - If the account has an OAuth token AND IsGmail is true, returns a
//     GmailAPISender — the Gmail API path is preferred over SMTP+XOAUTH2 because
//     the API echoes back a server-assigned Message-ID we can use for dedup
//     against the IMAP IDLE echo.
//   - Otherwise: SMTPSender, with provider-specific host and PLAIN auth via the
//     supplied AppPassword. For OAuth-Gmail accounts we never fall through to
//     this branch; non-Gmail OAuth providers are not supported in v1.
//
// The MS Graph branch is intentionally not selected automatically — see
// sender_graph.go.
func PickSender(_ context.Context, account SenderAccount, log *zerolog.Logger) (Sender, error) {
	if account.OAuthTokenSource != nil && account.IsGmail {
		return NewGmailAPISender(account.OAuthTokenSource, log), nil
	}
	info := SMTPInfoForEmail(account.Email)
	return NewSMTPSender(SMTPConfig{
		Host:       info.Host,
		Port:       info.Port,
		StartTLS:   info.StartTLS,
		Username:   account.Email,
		Password:   account.AppPassword,
		UseXOAUTH2: false,
	}, log), nil
}
