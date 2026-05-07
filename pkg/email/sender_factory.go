package email

import (
	"context"
	"net"
	"strings"
	"time"

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
//
// Google Workspace custom domains (e.g. an org's @company.com that uses Google
// for mail) aren't detectable from the address alone — caller should pass the
// MX-detection result via IsGoogleWorkspace to override the fallback. The IMAP
// client does this; outbound paths that don't have an MX-detection hook will
// silently fall back to smtp.<domain> which is wrong for Workspace accounts.
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

// SMTPInfoForGoogleWorkspace returns Gmail's SMTP submission endpoint for any
// domain that's confirmed (via MX lookup) to be Google Workspace-hosted. The
// caller is responsible for confirming the domain is Google-hosted; this helper
// just returns the canonical Gmail SMTP endpoint.
func SMTPInfoForGoogleWorkspace() SMTPProviderInfo {
	return SMTPProviderInfo{Host: "smtp.gmail.com", Port: 587, StartTLS: true}
}

// IsGoogleWorkspaceDomain reports whether the given domain's MX records point
// to Google Workspace mail servers. Returns false on any DNS failure or if no
// MX records are Google-shaped — caller should fall back to default behavior.
//
// Google Workspace MX patterns (as of 2025):
//   - aspmx.l.google.com
//   - alt1.aspmx.l.google.com .. alt4.aspmx.l.google.com
//   - smtp.google.com (newer)
//   - googlemail.l.google.com (legacy)
// All canonical hostnames end with ".google.com" after MX resolution.
func IsGoogleWorkspaceDomain(domain string) bool {
	resolver := &net.Resolver{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	mxs, err := resolver.LookupMX(ctx, domain)
	if err != nil || len(mxs) == 0 {
		return false
	}
	for _, mx := range mxs {
		host := strings.ToLower(strings.TrimSuffix(mx.Host, "."))
		if strings.HasSuffix(host, ".google.com") || strings.HasSuffix(host, ".googlemail.com") {
			return true
		}
	}
	return false
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
	Email              string             // user's primary email address (used as SMTP username and SASL identity)
	IsGmail            bool               // true for gmail.com / googlemail.com / Workspace accounts authorized via OAuth
	IsGoogleWorkspace  bool               // true when the domain's MX records point to Google — forces SMTP host to smtp.gmail.com regardless of domain
	AppPassword        string             // SMTP path: app password or normal password used with PLAIN auth
	OAuthTokenSource   oauth2.TokenSource // Gmail API path; nil when the account is SMTP-only
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
	var info SMTPProviderInfo
	if account.IsGoogleWorkspace {
		// Workspace custom domain (e.g. user@company.com hosted on Google) — IMAP+SMTP
		// live at gmail.com regardless of the user's domain.
		info = SMTPInfoForGoogleWorkspace()
	} else {
		info = SMTPInfoForEmail(account.Email)
	}
	return NewSMTPSender(SMTPConfig{
		Host:       info.Host,
		Port:       info.Port,
		StartTLS:   info.StartTLS,
		Username:   account.Email,
		Password:   account.AppPassword,
		UseXOAUTH2: false,
	}, log), nil
}
