package connector

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// OAuthListener is a transient HTTP server that receives Google's OAuth 2.0
// authorization-code redirect on a loopback URL.
//
// Lifecycle: StartOAuthListener binds to 127.0.0.1:RANDOMPORT (per RFC 8252
// Native Apps), serves exactly one /callback request, validates the state
// query parameter against expectedState, and either delivers the code on
// CodeCh or a server error on ErrCh. The listener auto-shuts down after one
// successful delivery, after Close(), or after the configured timeout.
//
// All callbacks that don't carry the right state get a 400 response and are
// NOT delivered to CodeCh — important defense against another local user on
// a multi-tenant host racing to deliver a forged callback.
type OAuthListener struct {
	server      *http.Server
	listener    net.Listener
	expectState string

	// redirectURI is the exact URL passed to BuildAuthURL and required by
	// Google's token-exchange endpoint (it validates the redirectURI on
	// /token matches the one the auth URL was built with).
	redirectURI string

	codeCh chan string
	errCh  chan error
	once   sync.Once
	closed chan struct{}
}

// StartOAuthListener spins up the callback listener.
//
// addr should be "127.0.0.1:0" (default; OS picks a free port) or
// "127.0.0.1:NNNN" if the operator has a stable SSH-tunnel port configured
// in gmail_oauth.listener_address. We refuse non-loopback bind addresses
// because the loopback-only redirect is the security model.
//
// The returned *OAuthListener is owned by the caller; it must call
// Wait(ctx) to block on the callback (or get a timeout error) and Close()
// to release the port if the login is cancelled before the user authorizes.
func StartOAuthListener(addr, expectedState string, timeout time.Duration) (*OAuthListener, error) {
	if expectedState == "" {
		return nil, errors.New("oauth listener: empty expected state")
	}
	if addr == "" {
		addr = "127.0.0.1:0"
	}
	if !isLoopbackAddr(addr) {
		return nil, fmt.Errorf("oauth listener: refused non-loopback bind address %q (security: loopback redirect must stay on the local host)", addr)
	}
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("oauth listener: bind %s: %w", addr, err)
	}

	l := &OAuthListener{
		listener:    ln,
		expectState: expectedState,
		codeCh:      make(chan string, 1),
		errCh:       make(chan error, 1),
		closed:      make(chan struct{}),
	}

	// Compose the redirectURI from the actual bound address so the operator's
	// "127.0.0.1:0" config (or a port collision) is reflected back to Google.
	tcpAddr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		_ = ln.Close()
		return nil, errors.New("oauth listener: bound address is not TCP")
	}
	l.redirectURI = fmt.Sprintf("http://127.0.0.1:%d/callback", tcpAddr.Port)

	mux := http.NewServeMux()
	mux.HandleFunc("/callback", l.handleCallback)
	mux.HandleFunc("/", l.handleRoot)

	l.server = &http.Server{
		Handler: mux,
		// Conservative timeouts — the user's browser hits us once with a tiny
		// querystring; nothing should take long.
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
	}

	go func() {
		if err := l.server.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			select {
			case l.errCh <- fmt.Errorf("oauth listener serve: %w", err):
			default:
			}
		}
	}()

	go func() {
		select {
		case <-time.After(timeout):
			select {
			case l.errCh <- fmt.Errorf("oauth callback timed out after %s", timeout):
			default:
			}
			l.Close()
		case <-l.closed:
		}
	}()

	return l, nil
}

// RedirectURI is the exact URL the listener will accept callbacks on; pass it
// verbatim to BuildAuthURL and to ExchangeCode.
func (l *OAuthListener) RedirectURI() string {
	return l.redirectURI
}

// Inject delivers a code/state pair to the listener as if it had arrived via
// the loopback /callback endpoint. Used by the `!matrimail oauth paste-code`
// admin command for headless deployments where the user's browser cannot
// reach the bridge's loopback port at all (no SSH access for tunneling, etc).
//
// State is validated against expectState the same way handleCallback does it,
// so a stale or forged code can't bypass CSRF protection just because it came
// in via a bot command instead of HTTP. If a code has already been delivered
// (real callback or earlier inject), this returns an error rather than
// silently overwriting.
func (l *OAuthListener) Inject(code, state string) error {
	if state == "" || state != l.expectState {
		return errors.New("oauth inject: state parameter does not match the active login (URL is from a stale session?)")
	}
	if code == "" {
		return errors.New("oauth inject: empty code")
	}
	select {
	case l.codeCh <- code:
		return nil
	default:
		return errors.New("oauth inject: a code has already been delivered for this login")
	}
}

// Wait blocks until either a valid callback arrives, the listener errors out
// (timeout, server error, state mismatch), or ctx is cancelled.
//
// On success returns the authorization code; on failure returns the error.
// Always safe to call Close() afterwards (it's idempotent).
func (l *OAuthListener) Wait(ctx context.Context) (string, error) {
	select {
	case code := <-l.codeCh:
		return code, nil
	case err := <-l.errCh:
		return "", err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// Close shuts down the HTTP server and releases the bound port. Idempotent —
// safe to call from defer.
func (l *OAuthListener) Close() {
	l.once.Do(func() {
		close(l.closed)
		if l.server != nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = l.server.Shutdown(shutdownCtx)
		}
	})
}

// handleCallback is the actual /callback endpoint. Validates state and either
// delivers the code on codeCh or refuses with 400.
func (l *OAuthListener) handleCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	// Google sends ?error=access_denied if the user clicked Cancel on the
	// consent screen. Surface a friendly message and a clear error.
	if errCode := q.Get("error"); errCode != "" {
		desc := q.Get("error_description")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, oauthCallbackHTML, "Authorization failed",
			fmt.Sprintf("<p>Google reported <code>%s</code>: %s.</p><p>Run <code>!matrimail login</code> in your Matrix client to try again.</p>", htmlEscape(errCode), htmlEscape(desc)))
		select {
		case l.errCh <- fmt.Errorf("oauth callback: %s: %s", errCode, desc):
		default:
		}
		return
	}

	state := q.Get("state")
	code := q.Get("code")

	if state == "" || state != l.expectState {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, oauthCallbackHTML, "Invalid request",
			"<p>State parameter mismatch. This window can be closed.</p><p>If you didn't initiate a matrimail login, ignore this — no action was taken.</p>")
		// Do NOT deliver to codeCh — this could be a forged callback from
		// another local process.
		return
	}

	if code == "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprintf(w, oauthCallbackHTML, "Missing code",
			"<p>No <code>code</code> parameter in the callback URL.</p>")
		select {
		case l.errCh <- errors.New("oauth callback: missing code parameter"):
		default:
		}
		return
	}

	// Success — render a friendly close-this-tab page and deliver the code.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, oauthCallbackHTML, "Matrimail authorized",
		"<p>You may now close this tab and return to your Matrix client.</p><p>The bridge will continue automatically.</p>")

	select {
	case l.codeCh <- code:
	default:
		// Channel full — already delivered; ignore the duplicate.
	}
}

// handleRoot is a no-op handler for any path other than /callback. Returns a
// 404 with a small HTML body so a curious browser doesn't see a blank page.
func (l *OAuthListener) handleRoot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	_, _ = fmt.Fprintf(w, oauthCallbackHTML, "Not found",
		"<p>This endpoint is only for the matrimail OAuth callback. Nothing to see here.</p>")
}

// isLoopbackAddr returns true for "127.0.0.1:PORT", "[::1]:PORT", or "localhost:PORT".
// Refuses 0.0.0.0 / public IPs / wildcards — the loopback-only redirect is
// the security model; we'd rather error fast than bind to the wrong interface.
func isLoopbackAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	switch host {
	case "127.0.0.1", "::1", "localhost":
		return true
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return true
	}
	return false
}

// oauthCallbackHTML is the page the user's browser sees after the OAuth
// redirect. Single template, two interpolation slots: title and body. No
// scripts, no images, no external resources — keeps the loopback callback
// inert against drive-by content security incidents.
const oauthCallbackHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>%s — matrimail</title>
<style>
body{font-family:system-ui,-apple-system,Segoe UI,Roboto,sans-serif;max-width:560px;margin:4em auto;padding:0 1.5em;line-height:1.5;color:#222;background:#fafafa}
h1{font-size:1.4em;margin-bottom:.5em}
code{background:#eee;padding:1px 6px;border-radius:3px}
</style>
</head><body><h1>%s</h1>
</body></html>`

// htmlEscape minimally escapes the four characters that matter when rendering
// untrusted query-param text inside HTML. Avoids pulling in html/template for
// what's effectively a 5-line static page.
func htmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
	)
	return r.Replace(s)
}
