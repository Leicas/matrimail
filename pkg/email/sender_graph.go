package email

import (
	"context"
	"errors"
)

// GraphSender is a placeholder for the Microsoft Graph (Outlook/Hotmail/Live/
// Office365) Sender impl planned for v2. v1 uses the SMTPSender for these
// domains via SMTPInfoForEmail in sender_factory.go.
type GraphSender struct{}

// Provider returns the transport identifier "ms-graph".
func (s *GraphSender) Provider() string { return "ms-graph" }

// Close is a no-op.
func (s *GraphSender) Close() error { return nil }

// Send always returns an error indicating the v1 fallback path.
func (s *GraphSender) Send(_ context.Context, _ []byte, _ string, _ []string, _ string) (string, error) {
	return "", errors.New("ms-graph sender not implemented in v1; SMTP fallback is used for outlook.com / hotmail.com / live.com / office365.com")
}
