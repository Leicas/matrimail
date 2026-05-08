// Phase B addition: Sent-folder dedup store.
//
// When the bridge sends a message via the outbound Sender (Gmail API or SMTP)
// the same message will reappear on IMAP IDLE — the user's mail server stamps
// it into its Sent folder and we read it from there a few seconds later. To
// avoid double-posting the user's own message into Matrix, the sender records
// (receiver, message_id) -> matrix_event_id at send time, and the inbound
// processor short-circuits when it sees a hit.
//
// The table is local to matrimail (not part of bridgev2's schema) and uses
// the same *database.Database instance the rest of the connector queries.
package connector

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mau.fi/util/dbutil"
	"maunium.net/go/mautrix/bridgev2/database"
)

// dialectQuery rewrites "?" placeholders to "$1, $2, ..." for Postgres.
// SQLite passes through unchanged. Used by sent-dedup queries which need
// to be portable across both dialects (the bridgev2 Database doesn't do
// this rewrite for us). Question marks inside string literals are NOT
// handled — keep the queries here free of literal "?" characters.
func dialectQuery(d dbutil.Dialect, q string) string {
	if d != dbutil.Postgres {
		return q
	}
	var b strings.Builder
	b.Grow(len(q) + 8)
	n := 0
	for _, r := range q {
		if r == '?' {
			n++
			fmt.Fprintf(&b, "$%d", n)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// SentDedupQuery records and looks up our own outbound messages by their
// RFC 5322 Message-ID. The receiver is the bridgev2 user-login ID
// (e.g. "email:alice@example.com") so dedup is naturally scoped per account.
type SentDedupQuery struct {
	DB       *database.Database
	BridgeID string
}

// CreateTable creates the matrimail_sent_dedup table and supporting index.
// Idempotent — safe to call from EmailConnector.Init on every startup.
func (q *SentDedupQuery) CreateTable(ctx context.Context) error {
	if q == nil || q.DB == nil {
		return fmt.Errorf("SentDedupQuery: nil DB")
	}
	// sent_at uses BIGINT (not INTEGER) for the same reason as the
	// email_accounts oauth_* timestamps: Postgres INTEGER is 32-bit and would
	// overflow at the Y2038 boundary (and instantly if we ever switched this
	// to UnixNano). SQLite is fine either way.
	if _, err := q.DB.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS matrimail_sent_dedup (
			bridge_id     TEXT   NOT NULL,
			receiver      TEXT   NOT NULL,
			message_id    TEXT   NOT NULL,
			matrix_evt_id TEXT   NOT NULL,
			sent_at       BIGINT NOT NULL,
			PRIMARY KEY (bridge_id, receiver, message_id)
		)
	`); err != nil {
		return fmt.Errorf("create matrimail_sent_dedup: %w", err)
	}
	// Best-effort widening for any pre-existing Postgres deployments where
	// sent_at was created as INTEGER. SQLite's syntax differs and its affinity
	// is already 64-bit, so skip there. Errors are swallowed.
	if q.DB.Dialect == dbutil.Postgres {
		_, _ = q.DB.Exec(ctx, `ALTER TABLE matrimail_sent_dedup ALTER COLUMN sent_at TYPE BIGINT`)
	}
	if _, err := q.DB.Exec(ctx, `
		CREATE INDEX IF NOT EXISTS idx_matrimail_sent_dedup_sent_at
			ON matrimail_sent_dedup(sent_at)
	`); err != nil {
		return fmt.Errorf("create matrimail_sent_dedup index: %w", err)
	}
	return nil
}

// Record inserts (or replaces) a dedup entry for the given outbound message.
// receiver is the user-login ID; messageID is the raw RFC 5322 ID without
// surrounding angle brackets; matrixEvtID is the room event ID we created
// when posting the user's compose into Matrix.
func (q *SentDedupQuery) Record(ctx context.Context, receiver, messageID, matrixEvtID string) error {
	if q == nil || q.DB == nil {
		return fmt.Errorf("SentDedupQuery: nil DB")
	}
	if messageID == "" {
		return fmt.Errorf("SentDedupQuery.Record: empty message_id")
	}
	// ON CONFLICT DO UPDATE works on both SQLite 3.24+ and Postgres 9.5+.
	_, err := q.DB.Exec(ctx, dialectQuery(q.DB.Dialect, `
		INSERT INTO matrimail_sent_dedup
			(bridge_id, receiver, message_id, matrix_evt_id, sent_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (bridge_id, receiver, message_id)
		DO UPDATE SET matrix_evt_id = EXCLUDED.matrix_evt_id, sent_at = EXCLUDED.sent_at
	`), q.BridgeID, receiver, messageID, matrixEvtID, time.Now().Unix())
	return err
}

// Exists reports whether (receiver, messageID) was previously recorded by us.
// The inbound processor calls this before forwarding a message read from a
// Sent-mailbox; a hit means "we sent it ourselves; suppress."
func (q *SentDedupQuery) Exists(ctx context.Context, receiver, messageID string) (bool, error) {
	if q == nil || q.DB == nil {
		return false, fmt.Errorf("SentDedupQuery: nil DB")
	}
	if messageID == "" {
		return false, nil
	}
	rows, err := q.DB.Query(ctx, dialectQuery(q.DB.Dialect, `
		SELECT 1 FROM matrimail_sent_dedup
		WHERE bridge_id = ? AND receiver = ? AND message_id = ?
		LIMIT 1
	`), q.BridgeID, receiver, messageID)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	if rows.Next() {
		return true, nil
	}
	return false, rows.Err()
}

// Cleanup deletes dedup entries older than olderThan and returns the count.
// Run periodically (24h) from a background goroutine — the IMAP echo of an
// outbound send arrives within seconds, so a 30-day TTL is plenty of slack
// for slow Sent folders or backfills.
func (q *SentDedupQuery) Cleanup(ctx context.Context, olderThan time.Time) (int64, error) {
	if q == nil || q.DB == nil {
		return 0, fmt.Errorf("SentDedupQuery: nil DB")
	}
	res, err := q.DB.Exec(ctx, dialectQuery(q.DB.Dialect, `
		DELETE FROM matrimail_sent_dedup
		WHERE bridge_id = ? AND sent_at < ?
	`), q.BridgeID, olderThan.Unix())
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, nil // not all drivers populate this; treat as best-effort
	}
	return n, nil
}
