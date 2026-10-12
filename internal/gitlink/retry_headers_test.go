package gitlink

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestRetryAfterDateAndInvalidHeaders(t *testing.T) {
	future := time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)
	for _, tc := range []struct {
		header string
		future bool
	}{{future, true}, {"Wed, 01 Jan 2020 00:00:00 GMT", false}, {"nonsense", false}, {"-1", false}, {"0", false}, {"", false}} {
		resp := &http.Response{Header: make(http.Header)}
		resp.Header.Set("Retry-After", tc.header)
		got := retryAfter(resp)
		if tc.future {
			if got < 59*time.Minute || got > time.Hour {
				t.Errorf("date delay %v", got)
			}
		} else if got != 0 {
			t.Errorf("invalid/past header %q delayed by %v", tc.header, got)
		}
	}
}

func TestRateLimitResetAndTokenRedaction(t *testing.T) {
	if got := untilRateLimitReset("garbage"); got != 0 {
		t.Fatalf("invalid reset %v", got)
	}
	if got := untilRateLimitReset(strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)); got < 59*time.Minute || got > time.Hour {
		t.Fatalf("future reset %v", got)
	}
	if got := scrubToken("request rejected", ""); got != "request rejected" {
		t.Fatalf("empty token altered diagnostic: %q", got)
	}
	if got := scrubToken("synthetic-key rejected synthetic-key", "synthetic-key"); got != "[redacted] rejected [redacted]" {
		t.Fatalf("credential remained in error: %q", got)
	}
}
