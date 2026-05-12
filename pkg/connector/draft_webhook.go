package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/rs/zerolog"
)

// DraftRequest is the JSON body POSTed to the configured draft webhook when
// the user invokes !matrimail draft (or, in the future, the reaction
// trigger). Fields named with snake_case for n8n-side ergonomics.
//
// The receiving workflow has everything it needs to:
//
//  1. Pick the right Gmail account to draft from (Account)
//  2. Identify the thread to reply to (ThreadID + MessageID)
//  3. Optionally surface a user-provided directive (Instruction)
//
// `Source` is purely informational so the workflow can branch on origin —
// e.g. show a different style of draft when the trigger was a reaction
// vs an explicit command.
type DraftRequest struct {
	Account      string   `json:"account"`
	UserMXID     string   `json:"user_mxid"`
	ThreadID     string   `json:"thread_id,omitempty"`
	MessageID    string   `json:"message_id,omitempty"`
	Subject      string   `json:"subject,omitempty"`
	Participants []string `json:"participants,omitempty"`
	Instruction  string   `json:"instruction,omitempty"`
	Source       string   `json:"source"` // "command" or "reaction"
	RoomID       string   `json:"room_id,omitempty"`
}

// DraftResponse is the loosely-typed envelope we accept from the webhook.
// We only surface `status` and `message` back to the user; everything else
// is ignored.
type DraftResponse struct {
	Status  string `json:"status,omitempty"`
	Message string `json:"message,omitempty"`
}

// triggerDraftWebhook POSTs the JSON-encoded req to cfg.URL with an optional
// bearer token and returns the (best-effort) decoded response. Network errors
// and non-2xx HTTP statuses are wrapped and returned as errors so the caller
// can surface them in the Matrix reply.
//
// The webhook is treated as fire-and-forget: we only wait for the immediate
// HTTP response, not for the workflow to actually finish drafting. If the
// receiver wants to push a status update back to the user, it should do so
// via the bot's own messaging path, not via this response.
func triggerDraftWebhook(ctx context.Context, cfg DraftWebhookConfig, req DraftRequest, logger *zerolog.Logger) (*DraftResponse, error) {
	if strings.TrimSpace(cfg.URL) == "" {
		return nil, fmt.Errorf("draft webhook not configured (set draft_webhook.url in config.yaml)")
	}

	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal draft request: %w", err)
	}

	postCtx, cancel := context.WithTimeout(ctx, cfg.EffectiveTimeout())
	defer cancel()

	httpReq, err := http.NewRequestWithContext(postCtx, http.MethodPost, cfg.URL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build draft request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "matrimail/draft-trigger")
	if cfg.Secret != "" {
		httpReq.Header.Set("Authorization", "Bearer "+cfg.Secret)
	}

	logger.Debug().Str("url", cfg.URL).Str("account", req.Account).Str("source", req.Source).Msg("Firing draft webhook")
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("draft webhook POST: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("draft webhook returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	parsed := &DraftResponse{}
	if len(respBody) > 0 {
		_ = json.Unmarshal(respBody, parsed) // best-effort; empty struct is fine
	}
	return parsed, nil
}
