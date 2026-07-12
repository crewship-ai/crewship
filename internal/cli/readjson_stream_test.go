package cli

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestReadJSONLargePayload guards the streaming-decoder rewrite: a payload in
// the megabyte range must decode without error (and, post-rewrite, without
// the intermediate full-body []byte copy the old ReadAll+Unmarshal made).
func TestReadJSONLargePayload(t *testing.T) {
	// ~2 MB of items — realistic large list response.
	var b strings.Builder
	b.WriteString(`[`)
	for i := 0; i < 20000; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"id":"cabcdefghijklmnopqrst","slug":"agent-slug-with-some-length","status":"IDLE","description":"padding padding padding"}`)
	}
	b.WriteString(`]`)

	resp := &http.Response{Body: io.NopCloser(strings.NewReader(b.String()))}
	var items []struct {
		ID     string `json:"id"`
		Slug   string `json:"slug"`
		Status string `json:"status"`
	}
	if err := ReadJSON(resp, &items); err != nil {
		t.Fatalf("ReadJSON large payload: %v", err)
	}
	if len(items) != 20000 {
		t.Errorf("decoded %d items, want 20000", len(items))
	}
}

// TestReadJSONOversizeCapped: a body past the 10 MB safety cap must error,
// never silently truncate into a half-decoded value.
func TestReadJSONOversizeCapped(t *testing.T) {
	huge := `{"pad":"` + strings.Repeat("x", 11<<20) + `"}`
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(huge))}
	var v struct {
		Pad string `json:"pad"`
	}
	if err := ReadJSON(resp, &v); err == nil {
		t.Fatal("expected error for >10MB response body")
	}
}

// TestReadJSONTrailingWhitespace: responses with trailing newline (common)
// must keep decoding.
func TestReadJSONTrailingWhitespace(t *testing.T) {
	resp := &http.Response{Body: io.NopCloser(strings.NewReader(`{"ok":true}` + "\n"))}
	var v struct {
		OK bool `json:"ok"`
	}
	if err := ReadJSON(resp, &v); err != nil {
		t.Fatalf("trailing whitespace broke decode: %v", err)
	}
	if !v.OK {
		t.Error("value not decoded")
	}
}
