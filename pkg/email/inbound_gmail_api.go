package email

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
	"golang.org/x/oauth2"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"

	gmail "google.golang.org/api/gmail/v1"
)

// GmailHistoryPoller polls a single Gmail account via the Gmail API for new
// messages. Runs as a long-lived goroutine spawned from the connector's
// LoadUserLogin; one poller per modify-mode account.
//
// Strategy:
//
//   - On first run with no persisted historyId, call users.getProfile to get
//     the current historyId and persist it as the cursor — we only ingest
//     forward from "now", not the entire mailbox history.
//   - Every PollInterval, call users.history.list(startHistoryId=cursor,
//     historyTypes=[messageAdded,labelAdded], labelId=monitored). For each
//     new messageId, fetch via users.messages.get(format=full) and hand the
//     gmail.Message to the per-message callback (which builds a ParsedEmail
//     and feeds the processor). labelAdded is required so that post-arrival
//     tagging (e.g. a separate Gmail filter or n8n workflow that applies the
//     monitored label after delivery) is still surfaced to Matrix.
//   - Persist the new historyId after each successful tick.
//
// Errors during a tick are logged and the cursor stays put — the next tick
// retries from the same cursor. Refresh-token failures (invalid_grant) bubble
// out of the TokenSource and are caught by the wrapping reauthAwareTokenSource
// in the connector layer; the poller just sees the error, logs, and the next
// poll either succeeds (transient) or hits the same error (the connector's
// re-auth path will handle it).
type GmailHistoryPoller struct {
	// Identity / persistence keys. Used by the cursor save/load callbacks.
	UserMXID string
	Email    string

	// MonitoredLabelIDs are the Gmail label IDs the user picked at login time.
	// users.history.list filters server-side: we only see messageAdded events
	// whose message acquired one of these labels. Use Gmail label IDs (e.g.
	// "INBOX", "Label_5") not display names.
	MonitoredLabelIDs []string

	// PollInterval is how long to wait between polls. Default 30s if unset.
	// Below 5s the server may rate-limit and the bridge wastes API quota.
	PollInterval time.Duration

	// TokenSource is the refresh-aware OAuth source. The connector wraps it
	// with reauthAwareTokenSource so refresh failures fire the re-auth UX.
	TokenSource oauth2.TokenSource

	// CursorLoad / CursorSave persist the lastHistoryId across restarts. The
	// connector wires these to its DB.GetGmailHistoryID / SetGmailHistoryID.
	// Both are required.
	CursorLoad func(ctx context.Context) (uint64, error)
	CursorSave func(ctx context.Context, historyID uint64) error

	// OnMessage is called for each newly-discovered message, after a
	// users.messages.get(format=full) round-trip. Errors are logged but don't
	// stop the poller. Required.
	OnMessage func(ctx context.Context, msg *gmail.Message, mailbox string) error

	Log *zerolog.Logger

	// Internal state.
	stopOnce sync.Once
	stopCh   chan struct{}
}

// Run blocks until ctx is cancelled or Stop() is called. Spawn it in a
// goroutine from the connector. Returns the first non-recoverable error
// encountered (e.g. ctx cancellation), or nil on graceful shutdown.
func (g *GmailHistoryPoller) Run(ctx context.Context) error {
	if err := g.validate(); err != nil {
		return err
	}
	g.stopCh = make(chan struct{})

	if g.PollInterval <= 0 {
		g.PollInterval = 30 * time.Second
	}

	logger := g.Log.With().Str("component", "gmail_history_poller").Str("email", g.Email).Logger()

	// Bootstrap: ensure we have a historyId cursor.
	cursor, err := g.bootstrapCursor(ctx)
	if err != nil {
		return fmt.Errorf("bootstrap cursor: %w", err)
	}
	logger.Info().Uint64("history_id", cursor).Msg("Gmail history poller starting")

	ticker := time.NewTicker(g.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info().Msg("Gmail history poller stopping (context cancelled)")
			return ctx.Err()
		case <-g.stopCh:
			logger.Info().Msg("Gmail history poller stopping (Stop called)")
			return nil
		case <-ticker.C:
			newCursor, perr := g.pollOnce(ctx, cursor, &logger)
			if perr != nil {
				// invalid_grant style: bubble up so the caller's wrapping
				// TokenSource can fire the re-auth UX. The connector wraps
				// our TokenSource with reauthAwareTokenSource, so the
				// bridge-level reauth path is wired automatically.
				logger.Warn().Err(perr).Msg("Gmail history poll tick failed; will retry next interval")
				continue
			}
			if newCursor != cursor {
				if err := g.CursorSave(ctx, newCursor); err != nil {
					logger.Warn().Err(err).Uint64("history_id", newCursor).Msg("Failed to persist Gmail historyId cursor")
				} else {
					cursor = newCursor
				}
			}
		}
	}
}

// Stop signals the poller to exit on its next iteration. Safe to call from
// any goroutine and idempotent.
func (g *GmailHistoryPoller) Stop() {
	g.stopOnce.Do(func() {
		if g.stopCh != nil {
			close(g.stopCh)
		}
	})
}

// validate returns an error for any missing required field.
func (g *GmailHistoryPoller) validate() error {
	if g.Email == "" {
		return errors.New("GmailHistoryPoller: empty Email")
	}
	if g.TokenSource == nil {
		return errors.New("GmailHistoryPoller: nil TokenSource")
	}
	if g.CursorLoad == nil || g.CursorSave == nil {
		return errors.New("GmailHistoryPoller: missing CursorLoad/CursorSave callbacks")
	}
	if g.OnMessage == nil {
		return errors.New("GmailHistoryPoller: nil OnMessage callback")
	}
	if g.Log == nil {
		return errors.New("GmailHistoryPoller: nil Log")
	}
	return nil
}

// bootstrapCursor returns the persisted historyId, or — if there isn't one —
// fetches the current historyId from users.getProfile and persists it before
// returning. This makes a fresh login start ingesting from "now" rather than
// trying to backfill the entire mailbox.
func (g *GmailHistoryPoller) bootstrapCursor(ctx context.Context) (uint64, error) {
	cursor, err := g.CursorLoad(ctx)
	if err != nil {
		return 0, fmt.Errorf("load cursor: %w", err)
	}
	if cursor != 0 {
		return cursor, nil
	}
	svc, err := g.gmailService(ctx)
	if err != nil {
		return 0, err
	}
	prof, err := svc.Users.GetProfile("me").Context(ctx).Do()
	if err != nil {
		return 0, fmt.Errorf("getProfile: %w", err)
	}
	cursor = prof.HistoryId
	if err := g.CursorSave(ctx, cursor); err != nil {
		return 0, fmt.Errorf("save initial cursor: %w", err)
	}
	return cursor, nil
}

// pollOnce runs a single history.list iteration starting from the given
// cursor, fans out users.messages.get for each new message, and returns the
// new historyId to persist (or `cursor` unchanged if no events).
func (g *GmailHistoryPoller) pollOnce(ctx context.Context, cursor uint64, logger *zerolog.Logger) (uint64, error) {
	svc, err := g.gmailService(ctx)
	if err != nil {
		return cursor, err
	}

	call := svc.Users.History.List("me").
		StartHistoryId(cursor).
		HistoryTypes("messageAdded", "labelAdded").
		Context(ctx)
	for _, lblID := range g.MonitoredLabelIDs {
		call = call.LabelId(lblID)
	}

	resp, err := call.Do()
	if err != nil {
		// historyId expired (Gmail keeps history for 7-30 days). When that
		// happens, refresh the cursor to current via getProfile and skip the
		// gap — we can't recover the missed messages without a full scan.
		if isHistoryExpiredError(err) {
			logger.Warn().Err(err).Uint64("cursor", cursor).
				Msg("Gmail historyId cursor expired; resetting to current and skipping gap")
			prof, perr := svc.Users.GetProfile("me").Context(ctx).Do()
			if perr != nil {
				return cursor, fmt.Errorf("getProfile after history-expired: %w", perr)
			}
			return prof.HistoryId, nil
		}
		return cursor, fmt.Errorf("history.list: %w", err)
	}

	newCursor := cursor
	if resp.HistoryId != 0 {
		newCursor = resp.HistoryId
	}

	// Pagination: history.list can return a pageToken on busy mailboxes.
	// Drain it so we don't lose events between ticks.
	pages := []*gmail.ListHistoryResponse{resp}
	for resp.NextPageToken != "" {
		nextCall := svc.Users.History.List("me").
			StartHistoryId(cursor).
			HistoryTypes("messageAdded", "labelAdded").
			PageToken(resp.NextPageToken).
			Context(ctx)
		for _, lblID := range g.MonitoredLabelIDs {
			nextCall = nextCall.LabelId(lblID)
		}
		next, err := nextCall.Do()
		if err != nil {
			return newCursor, fmt.Errorf("history.list pagination: %w", err)
		}
		pages = append(pages, next)
		if next.HistoryId > newCursor {
			newCursor = next.HistoryId
		}
		resp = next
	}

	// Build a deduped set of new message IDs. Entries can repeat across history
	// records (e.g. label-add then mark-read on the same msg). Two event types
	// feed this set:
	//
	//   - messagesAdded: the message was just delivered with a monitored label
	//     already applied (server-side filter, immediate-on-arrival labeling).
	//   - labelsAdded:   a monitored label was applied to an existing message
	//     after delivery (n8n classification workflow, user manual tag, etc.).
	//     We only count this if the newly-added labels intersect the monitored
	//     set — the labelId filter on history.list matches messages that
	//     currently have a monitored label, so it can also surface labelAdded
	//     events where some unrelated label was added to an already-monitored
	//     message; we don't want to re-forward in that case.
	//
	// Drafts (DRAFT label) are skipped — they don't have stable Message-IDs
	// and would otherwise appear in Matrix as messages from a third-party
	// ghost. The user composes drafts in Gmail; they only become visible in
	// Matrix once actually sent (at which point Gmail removes DRAFT).
	newMsgIDs := map[string]string{} // messageId → primary label (best-effort)
	for _, page := range pages {
		for _, h := range page.History {
			for _, ma := range h.MessagesAdded {
				if ma == nil || ma.Message == nil || ma.Message.Id == "" {
					continue
				}
				if hasLabel(ma.Message.LabelIds, "DRAFT") {
					logger.Debug().Str("gmail_msg_id", ma.Message.Id).Msg("Skipping draft message (DRAFT label)")
					continue
				}
				lbl := primaryLabel(ma.Message.LabelIds, g.MonitoredLabelIDs)
				newMsgIDs[ma.Message.Id] = lbl
			}
			for _, la := range h.LabelsAdded {
				if la == nil || la.Message == nil || la.Message.Id == "" {
					continue
				}
				if hasLabel(la.Message.LabelIds, "DRAFT") {
					logger.Debug().Str("gmail_msg_id", la.Message.Id).Msg("Skipping draft message (DRAFT label) on labelsAdded")
					continue
				}
				if !anyLabelMatches(la.LabelIds, g.MonitoredLabelIDs) {
					continue
				}
				if _, alreadyQueued := newMsgIDs[la.Message.Id]; alreadyQueued {
					continue
				}
				newMsgIDs[la.Message.Id] = primaryLabel(la.Message.LabelIds, g.MonitoredLabelIDs)
			}
		}
	}

	if len(newMsgIDs) == 0 {
		return newCursor, nil
	}
	logger.Debug().Int("new_messages", len(newMsgIDs)).Uint64("from", cursor).Uint64("to", newCursor).
		Msg("Gmail history poll: fetching new messages")

	for msgID, mailbox := range newMsgIDs {
		full, err := svc.Users.Messages.Get("me", msgID).Format("full").Context(ctx).Do()
		if err != nil {
			logger.Warn().Err(err).Str("gmail_msg_id", msgID).Msg("Failed to fetch Gmail message; skipping")
			continue
		}
		if cberr := g.OnMessage(ctx, full, mailbox); cberr != nil {
			logger.Warn().Err(cberr).Str("gmail_msg_id", msgID).Msg("OnMessage callback failed; continuing")
		}
	}
	return newCursor, nil
}

// gmailService constructs a fresh gmail.Service per call. The underlying
// http client is owned by the service object; constructing each call is
// trivial vs the round-trip it's about to do.
func (g *GmailHistoryPoller) gmailService(ctx context.Context) (*gmail.Service, error) {
	svc, err := gmail.NewService(ctx, option.WithTokenSource(g.TokenSource))
	if err != nil {
		return nil, fmt.Errorf("gmail.NewService: %w", err)
	}
	return svc, nil
}

// isHistoryExpiredError reports whether err is Gmail's "historyId is too
// old" 404. Per Google's docs the API returns 404 with reason "notFound" when
// the supplied startHistoryId is older than the server-side retention window
// (7 days for new accounts, longer for active ones).
func isHistoryExpiredError(err error) bool {
	if err == nil {
		return false
	}
	var gerr *googleapi.Error
	if errors.As(err, &gerr) {
		if gerr.Code == 404 {
			return true
		}
	}
	msg := err.Error()
	return strings.Contains(msg, "historyId") && strings.Contains(strings.ToLower(msg), "not found")
}

// hasLabel reports whether labels contains target (case-insensitive). Used to
// detect Gmail system labels like DRAFT, SENT, INBOX without depending on
// label-ID order.
func hasLabel(labels []string, target string) bool {
	for _, l := range labels {
		if strings.EqualFold(l, target) {
			return true
		}
	}
	return false
}

// anyLabelMatches reports whether any element of a equals any element of b
// (case-insensitive). Used to test whether a labelsAdded event added a
// monitored label, vs adding some unrelated label to an already-monitored
// message.
func anyLabelMatches(a, b []string) bool {
	for _, x := range a {
		for _, y := range b {
			if strings.EqualFold(x, y) {
				return true
			}
		}
	}
	return false
}

// primaryLabel picks the most-specific monitored label for a message. If the
// message has multiple monitored labels, prefer INBOX > others > first match.
// Falls back to the first monitored label match, or "" if none.
//
// The mailbox string is passed through to OnMessage as the "mailbox" arg of
// the processor — used downstream to decide things like "is this our Sent
// echo?". For Gmail-API mode we treat SENT specially: messages with the SENT
// label are outbound echoes.
func primaryLabel(messageLabels, monitored []string) string {
	for _, lbl := range messageLabels {
		if strings.EqualFold(lbl, "SENT") {
			return "SENT"
		}
		if strings.EqualFold(lbl, "INBOX") {
			return "INBOX"
		}
	}
	for _, lbl := range messageLabels {
		for _, mon := range monitored {
			if strings.EqualFold(lbl, mon) {
				return lbl
			}
		}
	}
	if len(monitored) > 0 {
		return monitored[0]
	}
	return ""
}
