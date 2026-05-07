package connector

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestOAuthListener_HappyPath(t *testing.T) {
	t.Parallel()
	l, err := StartOAuthListener("127.0.0.1:0", "S", 5*time.Second)
	if err != nil {
		t.Fatalf("StartOAuthListener: %v", err)
	}
	defer l.Close()

	if !strings.HasPrefix(l.RedirectURI(), "http://127.0.0.1:") {
		t.Errorf("redirect uri %q not loopback", l.RedirectURI())
	}

	go func() {
		// Simulate the browser hitting the callback after the OAuth dance.
		time.Sleep(50 * time.Millisecond)
		_, _ = http.Get(l.RedirectURI() + "?state=S&code=THE_CODE")
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	code, err := l.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if code != "THE_CODE" {
		t.Errorf("code = %q, want THE_CODE", code)
	}
}

func TestOAuthListener_StateMismatch(t *testing.T) {
	t.Parallel()
	l, err := StartOAuthListener("127.0.0.1:0", "EXPECTED", 5*time.Second)
	if err != nil {
		t.Fatalf("StartOAuthListener: %v", err)
	}
	defer l.Close()

	go func() {
		time.Sleep(50 * time.Millisecond)
		// Wrong state — should be rejected and NOT delivered to codeCh.
		resp, _ := http.Get(l.RedirectURI() + "?state=WRONG&code=ATTACK")
		if resp != nil && resp.StatusCode != http.StatusBadRequest {
			t.Errorf("state mismatch: status = %d, want 400", resp.StatusCode)
		}
		if resp != nil {
			_ = resp.Body.Close()
		}
	}()

	// The listener should NOT deliver the bogus code. Use a short timeout
	// here so the test fails fast if the security check doesn't work.
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	code, err := l.Wait(ctx)
	if err == nil && code != "" {
		t.Errorf("listener delivered code %q despite state mismatch", code)
	}
	// ctx.Err() (DeadlineExceeded) is the expected "no callback" path.
}

func TestOAuthListener_Timeout(t *testing.T) {
	t.Parallel()
	l, err := StartOAuthListener("127.0.0.1:0", "S", 100*time.Millisecond)
	if err != nil {
		t.Fatalf("StartOAuthListener: %v", err)
	}
	defer l.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()
	_, err = l.Wait(ctx)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("error %q does not mention timeout", err.Error())
	}
}

func TestOAuthListener_RefuseNonLoopback(t *testing.T) {
	t.Parallel()
	if _, err := StartOAuthListener("0.0.0.0:0", "S", 5*time.Second); err == nil {
		t.Errorf("expected error binding to 0.0.0.0")
	}
	if _, err := StartOAuthListener("8.8.8.8:0", "S", 5*time.Second); err == nil {
		t.Errorf("expected error binding to public IP")
	}
}

func TestOAuthListener_AccessDenied(t *testing.T) {
	t.Parallel()
	// Google sends ?error=access_denied if the user clicked Cancel.
	l, err := StartOAuthListener("127.0.0.1:0", "S", 5*time.Second)
	if err != nil {
		t.Fatalf("StartOAuthListener: %v", err)
	}
	defer l.Close()

	go func() {
		time.Sleep(50 * time.Millisecond)
		_, _ = http.Get(l.RedirectURI() + "?error=access_denied&error_description=user+cancelled&state=S")
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = l.Wait(ctx)
	if err == nil {
		t.Fatal("expected error for access_denied callback")
	}
	if !strings.Contains(err.Error(), "access_denied") {
		t.Errorf("error %q does not mention access_denied", err.Error())
	}
}

func TestOAuthListener_CloseIdempotent(t *testing.T) {
	t.Parallel()
	l, err := StartOAuthListener("127.0.0.1:0", "S", 5*time.Second)
	if err != nil {
		t.Fatalf("StartOAuthListener: %v", err)
	}
	l.Close()
	l.Close() // must not panic
	l.Close()
}

func TestIsLoopbackAddr(t *testing.T) {
	t.Parallel()
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:0", true},
		{"127.0.0.1:8888", true},
		{"localhost:8888", true},
		{"[::1]:8888", true},
		{"0.0.0.0:8888", false},
		{"192.168.1.1:8888", false},
		{"example.com:8888", false},
		{"not-an-addr", false},
	}
	for _, tc := range cases {
		if got := isLoopbackAddr(tc.addr); got != tc.want {
			t.Errorf("isLoopbackAddr(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}
