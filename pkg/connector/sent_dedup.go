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
	"time"

	"maunium.net/go/mautrix/bridgev2/database"
)

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
	if _, err := q.DB.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS matrimail_sent_dedup (
			bridge_id     TEXT    NOT NULL,
			receiver      TEXT    NOT NULL,
			message_id    TEXT    NOT NULL,
			matrix_evt_id TEXT    NOT NULL,
			sent_at       INTEGER NOT NULL,
			PRIMARY KEY (bridge_id, receiver, message_id)
		)
	`); err != nil {
		return fmt.Errorf("create matrimail_sent_dedup: %w", err)
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
	_, err := q.DB.Exec(ctx, `
		INSERT OR REPLACE INTO matrimail_sent_dedup
			(bridge_id, receiver, message_id, matrix_evt_id, sent_at)
		VALUES (?, ?, ?, ?, ?)
	`, q.BridgeID, receiver, messageID, matrixEvtID, time.Now().Unix())
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
	rows, err := q.DB.Query(ctx, `
		SELECT 1 FROM matrimail_sent_dedup
		WHERE bridge_id = ? AND receiver = ? AND message_id = ?
		LIMIT 1
	`, q.BridgeID, receiver, messageID)
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
	res, err := q.DB.Exec(ctx, `
		DELETE FROM matrimail_sent_dedup
		WHERE bridge_id = ? AND sent_at < ?
	`, q.BridgeID, olderThan.Unix())
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, nil // not all drivers populate this; treat as best-effort
	}
	return n, nil
}
