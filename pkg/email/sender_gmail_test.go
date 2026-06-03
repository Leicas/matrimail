package email

import (
	"errors"
	"testing"

	"google.golang.org/api/googleapi"
)

func TestIsGmailBadThreadErr(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "404 notFound (stale/cross-mailbox thread)",
			err:  &googleapi.Error{Code: 404, Message: "Requested entity was not found."},
			want: true,
		},
		{
			name: "400 mentioning thread",
			err:  &googleapi.Error{Code: 400, Message: "Invalid thread_id value"},
			want: true,
		},
		{
			name: "400 unrelated (bad recipient) must not retry",
			err:  &googleapi.Error{Code: 400, Message: "Invalid To header"},
			want: false,
		},
		{
			name: "401 auth error must not retry",
			err:  &googleapi.Error{Code: 401, Message: "Invalid Credentials"},
			want: false,
		},
		{
			name: "non-googleapi error",
			err:  errors.New("connection reset"),
			want: false,
		},
		{
			name: "wrapped 404 is detected via errors.As",
			err:  errInternalWrap{&googleapi.Error{Code: 404, Message: "not found"}},
			want: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isGmailBadThreadErr(tc.err); got != tc.want {
				t.Fatalf("isGmailBadThreadErr(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// errInternalWrap wraps an error so the test can exercise the errors.As path
// (fmt.Errorf("...: %w") produces an unexported *wrapError that we can't build
// directly).
type errInternalWrap struct{ inner error }

func (e errInternalWrap) Error() string { return "wrapped: " + e.inner.Error() }
func (e errInternalWrap) Unwrap() error { return e.inner }
