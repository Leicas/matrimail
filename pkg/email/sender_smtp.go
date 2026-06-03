package email

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"time"

	"github.com/rs/zerolog"
)

// SMTPConfig holds connection parameters for the SMTPSender. The Password field
// is overloaded: when UseXOAUTH2 is false it is treated as an app password used
// with PLAIN/LOGIN auth; when UseXOAUTH2 is true it is treated as an OAuth2
// access token presented via the XOAUTH2 SASL mechanism.
type SMTPConfig struct {
	Host       string
	Port       int
	Username   string
	Password   string // app password OR access token (when UseXOAUTH2 == true)
	StartTLS   bool
	UseXOAUTH2 bool
}

// SMTPSender implements Sender on top of net/smtp. It supports STARTTLS on 587,
// AUTH PLAIN with an app password, and AUTH XOAUTH2 with an OAuth2 access token.
type SMTPSender struct {
	cfg SMTPConfig
	log *zerolog.Logger
}

// NewSMTPSender constructs an SMTPSender. The provided logger is used for non-fatal
// diagnostics (e.g. partial recipient failures); pass a nil logger only in tests.
func NewSMTPSender(cfg SMTPConfig, log *zerolog.Logger) *SMTPSender {
	return &SMTPSender{cfg: cfg, log: log}
}

// Provider returns the transport identifier "smtp".
func (s *SMTPSender) Provider() string { return "smtp" }

// Close is a no-op; SMTP connections are short-lived per Send call.
func (s *SMTPSender) Close() error { return nil }

// Send delivers mimeBytes to the SMTP submission endpoint. SMTP does not echo
// back a server-assigned Message-ID, so the returned messageID is always "" —
// callers must read the Message-ID they wrote into the MIME headers instead.
//
// threadID is ignored: SMTP threading is governed entirely by the RFC
// In-Reply-To/References headers the MIME builder already wrote.
func (s *SMTPSender) Send(ctx context.Context, mimeBytes []byte, from string, to []string, _ string) (string, error) {
	if len(to) == 0 {
		return "", errors.New("smtp: no recipients")
	}
	if s.cfg.Host == "" {
		return "", errors.New("smtp: host not configured")
	}
	addr := net.JoinHostPort(s.cfg.Host, fmt.Sprintf("%d", s.cfg.Port))

	dialer := &net.Dialer{Timeout: 30 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return "", fmt.Errorf("smtp dial: %w", err)
	}

	c, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		_ = conn.Close()
		return "", fmt.Errorf("smtp client: %w", err)
	}
	defer func() { _ = c.Quit() }()

	if s.cfg.StartTLS {
		if err := c.StartTLS(&tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return "", fmt.Errorf("starttls: %w", err)
		}
	}

	if s.cfg.UseXOAUTH2 {
		if err := c.Auth(xoauth2Auth(s.cfg.Username, s.cfg.Password)); err != nil {
			return "", fmt.Errorf("smtp xoauth2 auth: %w", err)
		}
	} else {
		if err := c.Auth(smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)); err != nil {
			return "", fmt.Errorf("smtp auth: %w", err)
		}
	}

	if err := c.Mail(from); err != nil {
		return "", fmt.Errorf("smtp mail from: %w", err)
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return "", fmt.Errorf("smtp rcpt %s: %w", rcpt, err)
		}
	}
	w, err := c.Data()
	if err != nil {
		return "", fmt.Errorf("smtp data: %w", err)
	}
	if _, err := w.Write(mimeBytes); err != nil {
		_ = w.Close()
		return "", fmt.Errorf("smtp write: %w", err)
	}
	if err := w.Close(); err != nil {
		return "", fmt.Errorf("smtp close: %w", err)
	}

	// SMTP doesn't echo back a server-assigned Message-ID. The caller's MIME
	// builder is responsible for putting one in the headers and using that as
	// the dedup key.
	return "", nil
}

// xoauth2 implements the XOAUTH2 SASL mechanism for SMTP. The format is:
//
//	"user=" + email + "\x01auth=Bearer " + token + "\x01\x01"
//
// See https://developers.google.com/gmail/imap/xoauth2-protocol#smtp_protocol_exchange
type xoauth2 struct {
	user, token string
}

func xoauth2Auth(user, token string) smtp.Auth { return &xoauth2{user, token} }

func (x *xoauth2) Start(_ *smtp.ServerInfo) (string, []byte, error) {
	payload := []byte("user=" + x.user + "\x01auth=Bearer " + x.token + "\x01\x01")
	return "XOAUTH2", payload, nil
}

// Next is called if the server issues a continuation challenge. For XOAUTH2,
// a continuation indicates an auth failure; the spec asks the client to send
// an empty response so the server can return a structured error in the next
// 535 line. Returning a nil byte slice with no error matches what most clients
// (including go-imap's sasl client) do.
func (x *xoauth2) Next(_ []byte, _ bool) ([]byte, error) { return nil, nil }
