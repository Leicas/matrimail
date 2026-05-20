package email

import (
	"strings"
	"testing"
)

func TestIsLikelyDecorativeImage(t *testing.T) {
	cases := []struct {
		name string
		im   *InlineImageMeta
		want bool
	}{
		{"nil", nil, true},
		{"tiny gif", &InlineImageMeta{Size: 512, Label: "logo.gif"}, true},
		{"big photo", &InlineImageMeta{Size: 200 * 1024, Label: "photo.jpg"}, false},
		{"spacer label", &InlineImageMeta{Size: 50 * 1024, Label: "spacer.png"}, true},
		{"tracking label", &InlineImageMeta{Size: 50 * 1024, Label: "tracking-pixel.gif"}, true},
		{"1x1 label", &InlineImageMeta{Size: 50 * 1024, Label: "img-1x1.gif"}, true},
		{"normal screenshot", &InlineImageMeta{Size: 80 * 1024, Label: "screenshot.png"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isLikelyDecorativeImage(c.im); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestIsLikelyMarketingHTML(t *testing.T) {
	// Plain personal email — not marketing.
	personalHTML := `<html><body><p>Hi Antoine,</p><p>` +
		strings.Repeat("Just wanted to say thanks for the meeting yesterday. ", 30) +
		`</p></body></html>`
	if IsLikelyMarketingHTML(personalHTML, "Hi Antoine, just a quick thanks for the meeting yesterday.") {
		t.Errorf("personal email incorrectly classified as marketing")
	}

	// Small transactional HTML — not marketing (under size threshold).
	tiny := `<html><body><table><tr><td>Order #1234 confirmed.</td></tr></table></body></html>`
	if IsLikelyMarketingHTML(tiny, "Order #1234 confirmed.") {
		t.Errorf("small email incorrectly classified as marketing")
	}

	// Marketing-style: many tables + backgrounds + little text.
	var b strings.Builder
	b.WriteString(`<html><body>`)
	for i := 0; i < 12; i++ {
		b.WriteString(`<table cellpadding="0"><tr><td style="background-image: url(cid:bg` +
			string(rune('a'+i)) + `); width:600px"><a href="#">Click</a></td></tr></table>`)
	}
	b.WriteString(strings.Repeat(" ", 16*1024))
	b.WriteString(`</body></html>`)
	marketing := b.String()
	if !IsLikelyMarketingHTML(marketing, "Click Click Click") {
		t.Errorf("marketing email not detected; len=%d", len(marketing))
	}

	// Image-heavy newsletter (no backgrounds, lots of <img>) — still marketing.
	var nl strings.Builder
	nl.WriteString(`<html><body>`)
	for i := 0; i < 20; i++ {
		nl.WriteString(`<img src="cid:hero` + string(rune('a'+i%26)) + `" alt="">`)
	}
	nl.WriteString(strings.Repeat("x", 16*1024))
	nl.WriteString(`</body></html>`)
	if !IsLikelyMarketingHTML(nl.String(), "Read more") {
		t.Errorf("image-heavy newsletter not detected")
	}

	// Long-text marketing email (e.g., embedded blog post) — preserve HTML.
	longText := strings.Repeat("This is a long-form newsletter with real content. ", 50)
	if IsLikelyMarketingHTML(marketing, longText) {
		t.Errorf("long-text newsletter incorrectly classified as marketing (should preserve HTML)")
	}
}

func TestMaterializeBackgroundImages_CSS(t *testing.T) {
	in := `<table><tr><td style="background-image: url(cid:hero); padding: 10px">Click here</td></tr></table>`
	cidToMXC := map[string]string{"hero": "mxc://example.org/abc123"}
	got := MaterializeBackgroundImages(in, cidToMXC, nil)
	if !strings.Contains(got, `<img src="mxc://example.org/abc123"`) {
		t.Errorf("img tag not injected: %q", got)
	}
	// Original style is preserved (Matrix sanitizer will strip it; we keep
	// for fidelity in saved-HTML attachments).
	if !strings.Contains(got, "background-image") {
		t.Errorf("original style stripped unexpectedly: %q", got)
	}
}

func TestMaterializeBackgroundImages_OutlookAttr(t *testing.T) {
	in := `<table background="cid:bgimg"><tr><td>Hello</td></tr></table>`
	cidToMXC := map[string]string{"bgimg": "mxc://example.org/xyz789"}
	got := MaterializeBackgroundImages(in, cidToMXC, nil)
	if !strings.Contains(got, `<img src="mxc://example.org/xyz789"`) {
		t.Errorf("img tag not injected for background= attr: %q", got)
	}
}

func TestMaterializeBackgroundImages_UploadFallback(t *testing.T) {
	in := `<td style="background-image: url(cid:newcid)">x</td>`
	cidToMXC := map[string]string{}
	called := false
	uploader := func(cid string) (string, string) {
		called = true
		if cid != "newcid" {
			t.Errorf("uploader got cid %q, want newcid", cid)
		}
		return "mxc://example.org/uploaded", "image/png"
	}
	got := MaterializeBackgroundImages(in, cidToMXC, uploader)
	if !called {
		t.Errorf("uploader was not invoked")
	}
	if !strings.Contains(got, "mxc://example.org/uploaded") {
		t.Errorf("uploaded mxc not injected: %q", got)
	}
	if cidToMXC["newcid"] != "mxc://example.org/uploaded" {
		t.Errorf("cidToMXC not updated; got %v", cidToMXC)
	}
}

func TestMaterializeBackgroundImages_UnknownCID(t *testing.T) {
	in := `<td style="background-image: url(cid:missing)">x</td>`
	cidToMXC := map[string]string{}
	uploader := func(cid string) (string, string) { return "", "" }
	got := MaterializeBackgroundImages(in, cidToMXC, uploader)
	// Unchanged — no mxc available, nothing to inject.
	if strings.Contains(got, "<img") {
		t.Errorf("img injected despite uploader returning empty: %q", got)
	}
}

func TestMaterializeBackgroundImages_NoOpOnPlainHTML(t *testing.T) {
	in := `<p>Hello, <strong>world</strong>!</p>`
	got := MaterializeBackgroundImages(in, map[string]string{}, nil)
	if got != in {
		t.Errorf("plain HTML modified: in=%q out=%q", in, got)
	}
}
