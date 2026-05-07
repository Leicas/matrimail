package email

import "context"

// Sender is the abstraction over outbound email transports.
// Implementations must be safe for concurrent use.
type Sender interface {
	// Send delivers the given RFC 5322 MIME bytes. Returns the SERVER-ASSIGNED
	// Message-ID — for SMTP this is the one the caller put in the headers, but
	// for Gmail API the server may rewrite it; callers MUST use the returned
	// value as the dedup key, not the one they passed.
	Send(ctx context.Context, mimeBytes []byte, from string, to []string) (messageID string, err error)
	Provider() string // "smtp", "gmail-api", "ms-graph"
	Close() error
}
