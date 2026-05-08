package email

import (
	"strings"
	"testing"
)

func TestHTMLSignatureToText(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "whitespace only", in: "  \n\t  ", want: ""},
		{
			name: "plain text passthrough",
			in:   "Antoine\nHaply Robotics",
			want: "Antoine\nHaply Robotics",
		},
		{
			name: "br tags become newlines",
			in:   "Antoine<br>Haply Robotics<br/>antoine@haply.co",
			want: "Antoine\nHaply Robotics\nantoine@haply.co",
		},
		{
			name: "div blocks become newlines",
			in:   "<div>Antoine</div><div>Haply Robotics</div>",
			want: "Antoine\nHaply Robotics",
		},
		{
			name: "links stripped to anchor text",
			in:   `Antoine — <a href="https://haply.co">haply.co</a>`,
			want: "Antoine — haply.co",
		},
		{
			name: "html entities unescaped",
			in:   "Antoine &amp; team &lt;antoine@haply.co&gt;",
			want: "Antoine & team <antoine@haply.co>",
		},
		{
			name: "nested divs collapsed to two newlines",
			in:   "<div><div><div>L1</div></div></div><div>L2</div>",
			want: "L1\n\nL2",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := HTMLSignatureToText(tc.in)
			if got != tc.want {
				t.Errorf("HTMLSignatureToText(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestAppendSignature(t *testing.T) {
	t.Parallel()
	body := "Hello there"
	sig := "Antoine<br>Haply Robotics"

	t.Run("empty signature is no-op", func(t *testing.T) {
		t.Parallel()
		if got := AppendSignature(body, "", false); got != body {
			t.Errorf("plain: got %q, want unchanged %q", got, body)
		}
		if got := AppendSignature(body, "  \n  ", true); got != body {
			t.Errorf("html: whitespace-only signature should be no-op")
		}
	})

	t.Run("plain text uses RFC 3676 delimiter and converts HTML", func(t *testing.T) {
		t.Parallel()
		got := AppendSignature(body, sig, false)
		if !strings.Contains(got, "\n\n-- \n") {
			t.Errorf("missing RFC 3676 sig delimiter in %q", got)
		}
		if !strings.Contains(got, "Antoine\nHaply Robotics") {
			t.Errorf("HTML signature not converted to plain text in %q", got)
		}
	})

	t.Run("html keeps tags and uses html separator", func(t *testing.T) {
		t.Parallel()
		got := AppendSignature(body, sig, true)
		if !strings.Contains(got, "<br><br>-- <br>") {
			t.Errorf("missing HTML sig delimiter in %q", got)
		}
		if !strings.Contains(got, "Antoine<br>Haply Robotics") {
			t.Errorf("HTML signature got mangled in %q", got)
		}
	})
}
