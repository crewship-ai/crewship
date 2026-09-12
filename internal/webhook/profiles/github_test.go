package profiles

import (
	"strings"
	"testing"
)

// TestVerifyGitHubFixtures runs the minimum release fixture set §5 names:
// ping, and pull_request opened/reopened/synchronize.
func TestVerifyGitHubFixtures(t *testing.T) {
	tests := []struct {
		name       string
		file       string
		event      string
		delivery   string
		wantAction string
	}{
		{
			name:     "ping",
			file:     "github-ping.json",
			event:    "ping",
			delivery: "0b989ba4-242f-11e5-81e1-c7b6966d2516",
		},
		{
			name:       "pull_request opened",
			file:       "github-pull-request-opened.json",
			event:      "pull_request",
			delivery:   "72d3162e-cc78-11e3-81ab-4c9367dc0958",
			wantAction: "opened",
		},
		{
			name:       "pull_request reopened",
			file:       "github-pull-request-reopened.json",
			event:      "pull_request",
			delivery:   "72d3162e-cc78-11e3-81ab-4c9367dc0959",
			wantAction: "reopened",
		},
		{
			name:       "pull_request synchronize",
			file:       "github-pull-request-synchronize.json",
			event:      "pull_request",
			delivery:   "72d3162e-cc78-11e3-81ab-4c9367dc095a",
			wantAction: "synchronize",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := loadPayload(t, tt.file)
			got := Verify(VerifyRequest{
				Profile: ProfileGitHub,
				Header:  githubHeaders(githubSign(oldSecret, body), tt.event, tt.delivery),
				RawBody: body,
				Keys:    []Key{{ID: "key_old", Secret: []byte(oldSecret)}},
				Now:     fixedNow,
			})
			if !got.OK {
				t.Fatalf("rejected a valid delivery: %q", got.Reason)
			}
			if got.KeyID != "key_old" {
				t.Errorf("KeyID = %q, want key_old", got.KeyID)
			}
			if got.SourceEventID != tt.delivery {
				t.Errorf("SourceEventID = %q, want %q", got.SourceEventID, tt.delivery)
			}
			if got.EventType != tt.event {
				t.Errorf("EventType = %q, want %q", got.EventType, tt.event)
			}
			if got.Action != tt.wantAction {
				t.Errorf("Action = %q, want %q", got.Action, tt.wantAction)
			}
			if got.ContentKey != ContentKeyForBody(body) {
				t.Errorf("ContentKey = %q, want the body hash", got.ContentKey)
			}
			if !strings.HasPrefix(got.ContentKey, "sha256:") {
				t.Errorf("ContentKey %q is missing its scheme prefix", got.ContentKey)
			}
			if got.Reason != "" {
				t.Errorf("Reason = %q on an accepted delivery", got.Reason)
			}
		})
	}
}

// TestVerifyGitHubPingIsReportedNotDecided: the profile reports ping so the
// caller can answer 200/ignored, and does not itself decide anything about it.
func TestVerifyGitHubPingIsReportedNotDecided(t *testing.T) {
	body := loadPayload(t, "github-ping.json")
	got := Verify(VerifyRequest{
		Profile: ProfileGitHub,
		Header:  githubHeaders(githubSign(oldSecret, body), EventPing, "d-ping"),
		RawBody: body,
		Keys:    []Key{{ID: "key_old", Secret: []byte(oldSecret)}},
		Now:     fixedNow,
	})
	if !got.OK {
		t.Fatalf("a signed ping was rejected: %q", got.Reason)
	}
	if got.EventType != EventPing {
		t.Fatalf("EventType = %q, want %q", got.EventType, EventPing)
	}
	if got.Action != "" {
		t.Errorf("Action = %q, want empty: a ping payload has no action", got.Action)
	}
}

func TestVerifyGitHubRejections(t *testing.T) {
	body := loadPayload(t, "github-pull-request-opened.json")
	valid := githubSign(oldSecret, body)

	// One byte of the payload flipped: "opened" -> "openee". The header still
	// carries the signature of the original bytes.
	tampered := []byte(strings.Replace(string(body), `"action": "opened"`, `"action": "openee"`, 1))
	if string(tampered) == string(body) || len(tampered) != len(body) {
		t.Fatal("tamper fixture did not produce a same-length, different body")
	}

	tests := []struct {
		name       string
		signature  string
		event      string
		rawBody    []byte
		wantReason string
	}{
		{
			name:       "missing signature header",
			signature:  "",
			event:      "pull_request",
			rawBody:    body,
			wantReason: ReasonMissingSignatureHeader,
		},
		{
			name:       "no sha256= prefix",
			signature:  strings.TrimPrefix(valid, "sha256="),
			event:      "pull_request",
			rawBody:    body,
			wantReason: ReasonMalformedSignatureHeader,
		},
		{
			name:       "legacy sha1= prefix is not a second spelling",
			signature:  "sha1=" + strings.TrimPrefix(valid, "sha256="),
			event:      "pull_request",
			rawBody:    body,
			wantReason: ReasonMalformedSignatureHeader,
		},
		{
			name:       "uppercase prefix",
			signature:  "SHA256=" + strings.TrimPrefix(valid, "sha256="),
			event:      "pull_request",
			rawBody:    body,
			wantReason: ReasonMalformedSignatureHeader,
		},
		{
			name:       "not hex",
			signature:  "sha256=" + strings.Repeat("z", 64),
			event:      "pull_request",
			rawBody:    body,
			wantReason: ReasonMalformedSignatureHeader,
		},
		{
			name:       "hex but too short",
			signature:  "sha256=" + strings.TrimPrefix(valid, "sha256=")[:62],
			event:      "pull_request",
			rawBody:    body,
			wantReason: ReasonMalformedSignatureHeader,
		},
		{
			name:       "hex but too long",
			signature:  valid + "ab",
			event:      "pull_request",
			rawBody:    body,
			wantReason: ReasonMalformedSignatureHeader,
		},
		{
			name:       "empty after the prefix",
			signature:  "sha256=",
			event:      "pull_request",
			rawBody:    body,
			wantReason: ReasonMalformedSignatureHeader,
		},
		{
			name:       "signature from a different secret",
			signature:  githubSign("attacker-secret", body),
			event:      "pull_request",
			rawBody:    body,
			wantReason: ReasonSignatureMismatch,
		},
		{
			name:       "one byte of the body changed",
			signature:  valid,
			event:      "pull_request",
			rawBody:    tampered,
			wantReason: ReasonSignatureMismatch,
		},
		{
			name:       "signed body that is not JSON",
			signature:  githubSign(oldSecret, []byte("<html>not json</html>")),
			event:      "pull_request",
			rawBody:    []byte("<html>not json</html>"),
			wantReason: ReasonBodyNotJSON,
		},
		{
			name:       "signed body that is a JSON array",
			signature:  githubSign(oldSecret, []byte(`["opened"]`)),
			event:      "pull_request",
			rawBody:    []byte(`["opened"]`),
			wantReason: ReasonBodyNotJSON,
		},
		{
			name:       "signed body that is JSON null",
			signature:  githubSign(oldSecret, []byte(`null`)),
			event:      "pull_request",
			rawBody:    []byte(`null`),
			wantReason: ReasonBodyNotJSON,
		},
		{
			name:       "signed empty body",
			signature:  githubSign(oldSecret, nil),
			event:      "pull_request",
			rawBody:    nil,
			wantReason: ReasonBodyNotJSON,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Verify(VerifyRequest{
				Profile: ProfileGitHub,
				Header:  githubHeaders(tt.signature, tt.event, "d1"),
				RawBody: tt.rawBody,
				Keys:    []Key{{ID: "key_old", Secret: []byte(oldSecret)}},
				Now:     fixedNow,
			})
			if got.OK {
				t.Fatalf("accepted a request that should have been rejected: %+v", got)
			}
			if got.Reason != tt.wantReason {
				t.Errorf("Reason = %q, want %q", got.Reason, tt.wantReason)
			}
		})
	}
}

// TestVerifyGitHubPingToleratesANonObjectBody documents the one place §5's
// "not JSON when an action is expected" bites: ping expects no action, so a
// body that is not an object is not a rejection for that event.
func TestVerifyGitHubPingToleratesANonObjectBody(t *testing.T) {
	raw := []byte(`"pong"`)
	got := Verify(VerifyRequest{
		Profile: ProfileGitHub,
		Header:  githubHeaders(githubSign(oldSecret, raw), EventPing, "d-ping"),
		RawBody: raw,
		Keys:    []Key{{ID: "key_old", Secret: []byte(oldSecret)}},
		Now:     fixedNow,
	})
	if !got.OK {
		t.Fatalf("rejected a signed ping with a non-object body: %q", got.Reason)
	}
	if got.Action != "" {
		t.Errorf("Action = %q, want empty", got.Action)
	}
}

// TestVerifyGitHubContentKeyIgnoresUnsignedHeaders is the §5 replay rule: the
// signature covers neither the delivery id nor the event header, so neither may
// influence the replay key or the action.
func TestVerifyGitHubContentKeyIgnoresUnsignedHeaders(t *testing.T) {
	body := loadPayload(t, "github-pull-request-opened.json")
	signature := githubSign(oldSecret, body)
	keys := []Key{{ID: "key_old", Secret: []byte(oldSecret)}}

	first := Verify(VerifyRequest{
		Profile: ProfileGitHub,
		Header:  githubHeaders(signature, "pull_request", "72d3162e-cc78-11e3-81ab-4c9367dc0958"),
		RawBody: body,
		Keys:    keys,
		Now:     fixedNow,
	})
	// The same bytes redelivered under a fresh delivery id, which GitHub does
	// on a manual redeliver.
	redelivered := Verify(VerifyRequest{
		Profile: ProfileGitHub,
		Header:  githubHeaders(signature, "pull_request", "ffffffff-0000-1111-2222-333333333333"),
		RawBody: body,
		Keys:    keys,
		Now:     fixedNow,
	})
	if !first.OK || !redelivered.OK {
		t.Fatalf("a valid delivery was rejected: %q / %q", first.Reason, redelivered.Reason)
	}
	if first.SourceEventID == redelivered.SourceEventID {
		t.Fatal("fixture error: the two deliveries share a delivery id")
	}
	if first.ContentKey != redelivered.ContentKey {
		t.Errorf("a changed X-GitHub-Delivery changed the content key: %q != %q", first.ContentKey, redelivered.ContentKey)
	}

	// One byte different: a different delivery, and it must not collapse onto
	// the first one's replay key.
	other := []byte(strings.Replace(string(body), `"number": 2517`, `"number": 2518`, 1))
	if len(other) != len(body) || string(other) == string(body) {
		t.Fatal("fixture error: expected a same-length, one-field-different body")
	}
	changed := Verify(VerifyRequest{
		Profile: ProfileGitHub,
		Header:  githubHeaders(githubSign(oldSecret, other), "pull_request", "72d3162e-cc78-11e3-81ab-4c9367dc0958"),
		RawBody: other,
		Keys:    keys,
		Now:     fixedNow,
	})
	if !changed.OK {
		t.Fatalf("rejected a valid delivery of the changed body: %q", changed.Reason)
	}
	if changed.ContentKey == first.ContentKey {
		t.Error("two different bodies produced the same content key")
	}

	// A modified unsigned event header must not be able to turn identical bytes
	// into a different action.
	spoofed := Verify(VerifyRequest{
		Profile: ProfileGitHub,
		Header:  githubHeaders(signature, "issue_comment", "72d3162e-cc78-11e3-81ab-4c9367dc0958"),
		RawBody: body,
		Keys:    keys,
		Now:     fixedNow,
	})
	if !spoofed.OK {
		t.Fatalf("rejected a valid delivery: %q", spoofed.Reason)
	}
	if spoofed.Action != first.Action {
		t.Errorf("a changed X-GitHub-Event changed Action: %q != %q", spoofed.Action, first.Action)
	}
	if spoofed.ContentKey != first.ContentKey {
		t.Errorf("a changed X-GitHub-Event changed the content key")
	}
	if spoofed.EventType != "issue_comment" {
		// The event type IS reported as sent — it is unsigned, and the caller
		// is expected to treat it as a hint constrained by its allow-list.
		t.Errorf("EventType = %q, want the header as sent", spoofed.EventType)
	}
}

// TestContentKeyForBody covers the exported helper directly: same bytes, same
// key; one byte different, different key; and the empty body has a key too.
func TestContentKeyForBody(t *testing.T) {
	a := ContentKeyForBody([]byte(`{"action":"opened"}`))
	b := ContentKeyForBody([]byte(`{"action":"opened"}`))
	c := ContentKeyForBody([]byte(`{"action":"opener"}`))
	if a != b {
		t.Error("identical bodies produced different content keys")
	}
	if a == c {
		t.Error("different bodies produced the same content key")
	}
	if got := ContentKeyForBody(nil); !strings.HasPrefix(got, "sha256:") || len(got) != len("sha256:")+64 {
		t.Errorf("ContentKeyForBody(nil) = %q, want a sha256: hex key", got)
	}
}
