package pipeline

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"testing"
	"time"
)

func overlapSign(secret, msg string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(msg))
	return hex.EncodeToString(m.Sum(nil))
}

func TestWebhookOverlap_MatchSignature(t *testing.T) {
	expiry := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	const body = `{"a":1}`
	tests := []struct {
		name     string
		cur      string
		prev     string
		expires  *time.Time
		now      time.Time
		signWith string
		wantOK   bool
		wantPrev bool
	}{
		{"current always works", "new", "old", &expiry, expiry.Add(-time.Hour), "new", true, false},
		{"previous inside the window", "new", "old", &expiry, expiry.Add(-time.Nanosecond), "old", true, true},
		{"previous at the exact expiry is closed", "new", "old", &expiry, expiry, "old", false, false},
		{"previous after the expiry is closed", "new", "old", &expiry, expiry.Add(time.Second), "old", false, false},
		{"previous without an expiry is never accepted", "new", "old", nil, expiry, "old", false, false},
		{"empty previous never matches an empty-key signature", "new", "", &expiry, expiry.Add(-time.Hour), "", false, false},
		{"empty current never matches an empty-key signature", "", "old", &expiry, expiry.Add(-time.Hour), "", false, false},
		{"empty current, previous still open", "", "old", &expiry, expiry.Add(-time.Hour), "old", true, true},
		{"unknown secret", "new", "old", &expiry, expiry.Add(-time.Hour), "third", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := &Webhook{SigningSecret: tc.cur, PreviousSigningSecret: tc.prev, PreviousSecretExpiresAt: tc.expires}
			sig := overlapSign(tc.signWith, body)
			ok, prev := w.MatchSignature([]byte(body), sig, tc.now)
			if ok != tc.wantOK || prev != tc.wantPrev {
				t.Fatalf("MatchSignature = (%v,%v), want (%v,%v)", ok, prev, tc.wantOK, tc.wantPrev)
			}
			if ok2, _ := w.MatchSignature([]byte(body), "", tc.now); ok2 {
				t.Fatal("an empty signature must never verify")
			}
			// Timestamped scheme honours the same boundaries.
			ts := strconv.FormatInt(tc.now.Unix(), 10)
			tsSig := overlapSign(tc.signWith, ts+"."+body)
			ok, prev = w.MatchTimestampedSignature([]byte(body), ts, tsSig, tc.now, 0)
			if ok != tc.wantOK || prev != tc.wantPrev {
				t.Fatalf("MatchTimestampedSignature = (%v,%v), want (%v,%v)", ok, prev, tc.wantOK, tc.wantPrev)
			}
		})
	}
}
