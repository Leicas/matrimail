// Phase D: Portal.Metadata schema for matrimail.
//
// The framework gives connectors a free-form `Metadata any` slot on each
// Portal row that is JSON-serialized into the SQL `portal.metadata` column.
// We use it to round-trip enough thread state that synthetic compose threads
// (which only live in the in-memory ThreadManager cache, TTL 24h) survive a
// bridge restart or a long idle period.
//
// All fields are JSON-tagged with `omitempty` so an empty PortalMetadata
// serializes to `{}` rather than padding the column with NULL strings, which
// matters for forward-compatibility: future fields can be added without
// migrating older rows.
package connector

// PortalMetadata mirrors the parts of email.EmailThread that are useful to
// reconstruct a thread from cold storage. Kept narrow on purpose: the
// participant-delta fields (Added/Removed) are runtime-only churn from inbound
// threading and have no meaning at restore time.
type PortalMetadata struct {
	// ThreadID is the EmailThread.ThreadID. Stored explicitly so a defensive
	// reader can sanity-check that the metadata it loaded actually belongs to
	// the thread the portal claims to host (mismatches mean the row was
	// hand-edited or the schema drifted).
	ThreadID string `json:"thread_id,omitempty"`

	// Subject is the canonical subject line, with no Re:/Fwd: stripping. Used
	// when restoring a thread that has never received an inbound message
	// (i.e. a draft) so HandleMatrixMessage can build the outgoing Subject
	// without falling back to "(no subject)".
	Subject string `json:"subject,omitempty"`

	// Participants is the active participant set (To + Cc folded together,
	// without From). For compose threads this seeds the recipient list.
	Participants []string `json:"participants,omitempty"`

	// References is the threading chain (oldest first). After the first send
	// in a compose thread this gets populated with the new Message-ID and
	// is what subsequent replies thread against.
	References []string `json:"references,omitempty"`

	// LastMessageID is the most recent Message-ID we know of for this thread.
	// On send, we update this to the dedup key (server-assigned ID for Gmail
	// API, our generated ID for SMTP) so the next outbound reply has a tail
	// to thread against even when the in-memory ThreadManager has evicted.
	LastMessageID string `json:"last_message_id,omitempty"`

	// IsDraft marks a synthetic compose thread that has not yet produced an
	// outbound email. Cleared on first successful send.
	IsDraft bool `json:"is_draft,omitempty"`
}
