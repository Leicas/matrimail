// Phase D: compose flow.
//
// Two surfaces both arrive here:
//
//   - bridgev2.IdentifierResolvingNetworkAPI.ResolveIdentifier — invoked by
//     `start-chat` / `pm` / `resolve-identifier` bot commands and by the
//     bridge's provisioning HTTP API. Clients like Element or Beeper expose
//     "start a new chat" UI that funnels through here.
//   - !matrimail compose to:foo@bar.com [cc:...] [subject:"..."] — the bot
//     command in commands.go (handleCompose) wraps the same ResolveIdentifier
//     call and then attaches the parsed Cc/Subject to the synthetic thread.
//
// The output is a synthetic EmailThread (IsDraft = true) seeded into the
// ThreadManager cache plus a PortalKey the framework can materialize. Actual
// email send happens later from HandleMatrixMessage when the user types the
// first message in the freshly-created room (see client_send.go's IsDraft
// branch).
//
// Persistence: ResolveIdentifier only touches the in-memory cache. The
// Portal.Metadata write happens after the framework calls GetChatInfo (via
// connector.GetChatInfo) AND after the first send (via client_send.go), so
// the synthetic thread survives the 24h ThreadManager TTL.
package connector

import (
	"context"
	"fmt"
	netmail "net/mail"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"github.com/Leicas/matrimail/pkg/common"
	"github.com/Leicas/matrimail/pkg/email"
)

// Compile-time interface assertions. ResolveIdentifier lives on the per-login
// EmailClient (it needs UserLogin context); ValidateUserID lives on the
// connector (no per-user state needed). Together they let bridgev2 expose the
// "start chat" / `pm` flow without provisioning a real email at validation
// time.
var (
	_ bridgev2.IdentifierResolvingNetworkAPI = (*EmailClient)(nil)
	_ bridgev2.IdentifierValidatingNetwork   = (*EmailConnector)(nil)
)

// ValidateUserID reports whether a networkid.UserID looks syntactically like
// an email user ID this bridge would mint. Called by bridgev2 to vet ghost
// IDs before they reach ResolveIdentifier (e.g. when a Matrix user enters
// `@email_alice_at_example.com:beeper.com` directly). We accept anything that
// strips the "email:" prefix to a parseable RFC 5322 address; we deliberately
// don't probe DNS or connect to the destination — that's the recipient
// server's job at SMTP time.
func (ec *EmailConnector) ValidateUserID(id networkid.UserID) bool {
	s := strings.TrimPrefix(string(id), "email:")
	if s == string(id) {
		// No prefix => not one of ours.
		return false
	}
	_, err := netmail.ParseAddress(s)
	return err == nil
}

// ResolveIdentifier turns an email-address-shaped identifier into a ghost +
// synthetic compose portal. createChat=false is used by the
// `resolve-identifier` command (just info, no portal); createChat=true comes
// from `start-chat` / `pm` / our own `compose` command and produces a portal
// the caller will materialize via Bridge.GetPortalByKey + CreateMatrixRoom.
//
// Each invocation mints a NEW thread ID even for the same recipient: email
// compose semantics expect "new thread" rather than "rejoin existing thread"
// when a user clicks "compose" twice. Existing threads with that recipient
// are still accessible via inbound IDLE; the user is free to type in those
// rooms instead.
func (ec *EmailClient) ResolveIdentifier(ctx context.Context, identifier string, createChat bool) (*bridgev2.ResolveIdentifierResponse, error) {
	addr, err := netmail.ParseAddress(strings.TrimSpace(identifier))
	if err != nil {
		return nil, fmt.Errorf("matrimail: invalid email address %q: %w", identifier, err)
	}
	target := strings.ToLower(addr.Address)

	userID := common.EmailToGhostID(target)
	displayName := addr.Name
	if displayName == "" {
		displayName = target
	}
	info := &bridgev2.UserInfo{
		Name:        &displayName,
		Identifiers: []string{"email:" + target},
	}

	if !createChat {
		return &bridgev2.ResolveIdentifierResponse{
			UserID:   userID,
			UserInfo: info,
		}, nil
	}

	// Mint a synthetic thread ID. UnixNano + recipient gives us global
	// uniqueness without a DB round-trip; the "compose-" prefix is a hint
	// at debug time but carries no semantics.
	threadID := fmt.Sprintf("compose-%d-%s", time.Now().UnixNano(), target)
	portalKey := networkid.PortalKey{
		ID:       MakePortalID(threadID),
		Receiver: ec.UserLogin.ID,
	}

	// Pre-seed the in-memory thread. The first send in HandleMatrixMessage
	// will look this up via resolveThreadForPortal (which already understands
	// "thread:" prefix stripping) and consume the IsDraft flag.
	syntheticThread := &email.EmailThread{
		ThreadID:     threadID,
		Subject:      "",
		Participants: []string{target, ec.Email},
		IsDraft:      true,
		LastAccessed: time.Now(),
	}
	if ec.Main.ThreadManager != nil {
		ec.Main.ThreadManager.CacheForReceiver(string(ec.UserLogin.ID), syntheticThread)
	}

	return &bridgev2.ResolveIdentifierResponse{
		UserID:   userID,
		UserInfo: info,
		Chat: &bridgev2.CreateChatResponse{
			PortalKey: portalKey,
		},
	}, nil
}

// composeArgs is the parsed shape of a `!matrimail compose` invocation. Cc
// is optional; Subject is optional (defaults to "(no subject)" at send time
// if the user never sets one).
type composeArgs struct {
	To      string
	Cc      []string
	Subject string
}

// parseComposeArgs splits a !matrimail compose argument string into its
// constituent fields. Supports:
//
//	to:alice@example.com
//	cc:bob@example.com cc:carol@example.com
//	subject:"Hello world"
//
// Quoting rules match parseQuotedArgs (simple double-quote with backslash
// escapes). Unknown tokens are silently ignored — the caller decides whether
// to error on missing required fields.
func parseComposeArgs(raw string) (*composeArgs, error) {
	args := &composeArgs{}
	for _, tok := range parseQuotedArgs(strings.TrimSpace(raw)) {
		switch {
		case strings.HasPrefix(tok, "to:"):
			args.To = strings.TrimSpace(strings.TrimPrefix(tok, "to:"))
		case strings.HasPrefix(tok, "cc:"):
			cc := strings.TrimSpace(strings.TrimPrefix(tok, "cc:"))
			if cc != "" {
				args.Cc = append(args.Cc, cc)
			}
		case strings.HasPrefix(tok, "subject:"):
			args.Subject = strings.TrimPrefix(tok, "subject:")
		}
	}
	if args.To == "" {
		return nil, fmt.Errorf("missing required `to:` argument")
	}
	if _, err := netmail.ParseAddress(args.To); err != nil {
		return nil, fmt.Errorf("invalid `to:` address %q: %w", args.To, err)
	}
	for _, c := range args.Cc {
		if _, err := netmail.ParseAddress(c); err != nil {
			return nil, fmt.Errorf("invalid `cc:` address %q: %w", c, err)
		}
	}
	return args, nil
}
