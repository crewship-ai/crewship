package profiles

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fixedNow is the verification instant for every test that does not care about
// the clock. Nothing in this package reads time.Now, so this is the only clock
// there is.
var fixedNow = time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

const (
	oldSecret = "old-endpoint-secret-value"
	newSecret = "new-endpoint-secret-value"
)

// loadPayload reads a testdata payload as the exact bytes a sender would have
// signed. The files are never re-serialised: a round-trip through encoding/json
// would change the bytes and every signature over them.
func loadPayload(t *testing.T, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return body
}

// githubSign returns the X-Hub-Signature-256 value a sender holding secret
// would send for body.
func githubSign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// swSign returns the webhook-signature value for one v1 signature. key is the
// raw HMAC key, i.e. already past the whsec_/base64 convention.
func swSign(key []byte, msgID string, ts int64, body []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(msgID + "." + strconv.FormatInt(ts, 10) + "."))
	mac.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func githubHeaders(signature, event, delivery string) http.Header {
	h := http.Header{}
	if signature != "" {
		h.Set(HeaderGitHubSignature, signature)
	}
	if event != "" {
		h.Set(HeaderGitHubEvent, event)
	}
	if delivery != "" {
		h.Set(HeaderGitHubDelivery, delivery)
	}
	return h
}

func swHeaders(id, timestamp, signature string) http.Header {
	h := http.Header{}
	if id != "" {
		h.Set(HeaderStandardWebhooksID, id)
	}
	if timestamp != "" {
		h.Set(HeaderStandardWebhooksTimestamp, timestamp)
	}
	if signature != "" {
		h.Set(HeaderStandardWebhooksSignature, signature)
	}
	return h
}

func TestKeyRotation(t *testing.T) {
	body := loadPayload(t, "github-pull-request-opened.json")
	oldKey := Key{ID: "key_old", Secret: []byte(oldSecret)}
	newKey := Key{ID: "key_new", Secret: []byte(newSecret)}

	// An overlap that ended an hour ago, and one that ends in an hour.
	expiredOld := Key{ID: "key_old", Secret: []byte(oldSecret), NotAfter: fixedNow.Add(-time.Hour)}
	endingOld := Key{ID: "key_old", Secret: []byte(oldSecret), NotAfter: fixedNow.Add(time.Hour)}
	// NotAfter exactly at Now: the boundary is inclusive, so this key is still
	// good for this instant.
	edgeOld := Key{ID: "key_old", Secret: []byte(oldSecret), NotAfter: fixedNow}

	tests := []struct {
		name       string
		signedWith string
		keys       []Key
		wantOK     bool
		wantKeyID  string
		wantReason string
	}{
		{
			name:       "old key alone verifies its own signature",
			signedWith: oldSecret,
			keys:       []Key{oldKey},
			wantOK:     true,
			wantKeyID:  "key_old",
		},
		{
			name:       "new key alone rejects a signature from the old key",
			signedWith: oldSecret,
			keys:       []Key{newKey},
			wantReason: ReasonSignatureMismatch,
		},
		{
			name:       "overlap accepts the old key",
			signedWith: oldSecret,
			keys:       []Key{oldKey, newKey},
			wantOK:     true,
			wantKeyID:  "key_old",
		},
		{
			name:       "overlap accepts the new key",
			signedWith: newSecret,
			keys:       []Key{oldKey, newKey},
			wantOK:     true,
			wantKeyID:  "key_new",
		},
		{
			name:       "overlap accepts the new key when the old one is listed second",
			signedWith: newSecret,
			keys:       []Key{newKey, oldKey},
			wantOK:     true,
			wantKeyID:  "key_new",
		},
		{
			name:       "old key past NotAfter no longer verifies",
			signedWith: oldSecret,
			keys:       []Key{expiredOld, newKey},
			wantReason: ReasonSignatureMismatch,
		},
		{
			name:       "new key still verifies while the old one is expired",
			signedWith: newSecret,
			keys:       []Key{expiredOld, newKey},
			wantOK:     true,
			wantKeyID:  "key_new",
		},
		{
			name:       "old key inside a scheduled overlap still verifies",
			signedWith: oldSecret,
			keys:       []Key{endingOld, newKey},
			wantOK:     true,
			wantKeyID:  "key_old",
		},
		{
			name:       "NotAfter exactly at Now is still eligible",
			signedWith: oldSecret,
			keys:       []Key{edgeOld},
			wantOK:     true,
			wantKeyID:  "key_old",
		},
		{
			name:       "every key expired leaves nothing eligible",
			signedWith: oldSecret,
			keys:       []Key{expiredOld, {ID: "key_new", Secret: []byte(newSecret), NotAfter: fixedNow.Add(-time.Minute)}},
			wantReason: ReasonNoEligibleKey,
		},
		{
			name:       "no keys at all",
			signedWith: oldSecret,
			keys:       nil,
			wantReason: ReasonNoEligibleKey,
		},
		{
			name:       "empty secret is never a usable key",
			signedWith: oldSecret,
			keys:       []Key{{ID: "key_blank", Secret: nil}},
			wantReason: ReasonNoEligibleKey,
		},
		{
			name:       "a blank key alongside a real one does not disable the real one",
			signedWith: newSecret,
			keys:       []Key{{ID: "key_blank", Secret: []byte{}}, newKey},
			wantOK:     true,
			wantKeyID:  "key_new",
		},
		{
			name:       "more than two keys is a configuration error",
			signedWith: oldSecret,
			keys:       []Key{oldKey, newKey, {ID: "key_third", Secret: []byte("third")}},
			wantReason: ReasonTooManyKeys,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Verify(VerifyRequest{
				Profile: ProfileGitHub,
				Header:  githubHeaders(githubSign(tt.signedWith, body), "pull_request", "d1"),
				RawBody: body,
				Keys:    tt.keys,
				Now:     fixedNow,
			})
			if got.OK != tt.wantOK {
				t.Fatalf("OK = %v, want %v (reason %q)", got.OK, tt.wantOK, got.Reason)
			}
			if got.KeyID != tt.wantKeyID {
				t.Errorf("KeyID = %q, want %q", got.KeyID, tt.wantKeyID)
			}
			if got.Reason != tt.wantReason {
				t.Errorf("Reason = %q, want %q", got.Reason, tt.wantReason)
			}
		})
	}
}

// TestKeyRotationAppliesToStandardWebhooks repeats the rotation cases that
// matter for the other profile, including §5's requirement that a second key
// cannot buy a stale delivery past the freshness check.
func TestKeyRotationAppliesToStandardWebhooks(t *testing.T) {
	body := []byte(`{"type":"pr.opened","data":{"number":2517}}`)
	const msgID = "msg_rotation_case"
	ts := fixedNow.Unix()

	oldKey := Key{ID: "key_old", Secret: []byte(oldSecret)}
	newKey := Key{ID: "key_new", Secret: []byte(newSecret)}

	t.Run("old key inside the overlap", func(t *testing.T) {
		got := Verify(VerifyRequest{
			Profile: ProfileStandardWebhooks,
			Header:  swHeaders(msgID, strconv.FormatInt(ts, 10), swSign([]byte(oldSecret), msgID, ts, body)),
			RawBody: body,
			Keys:    []Key{oldKey, newKey},
			Now:     fixedNow,
		})
		if !got.OK || got.KeyID != "key_old" {
			t.Fatalf("got %+v, want OK with key_old", got)
		}
	})

	t.Run("new key inside the overlap", func(t *testing.T) {
		got := Verify(VerifyRequest{
			Profile: ProfileStandardWebhooks,
			Header:  swHeaders(msgID, strconv.FormatInt(ts, 10), swSign([]byte(newSecret), msgID, ts, body)),
			RawBody: body,
			Keys:    []Key{oldKey, newKey},
			Now:     fixedNow,
		})
		if !got.OK || got.KeyID != "key_new" {
			t.Fatalf("got %+v, want OK with key_new", got)
		}
	})

	t.Run("rotation does not bypass the timestamp check", func(t *testing.T) {
		// Perfectly signed by the freshly rotated key, but an hour old. The
		// timestamp is checked before any key is consulted, so having two keys
		// changes nothing.
		stale := fixedNow.Add(-time.Hour).Unix()
		got := Verify(VerifyRequest{
			Profile: ProfileStandardWebhooks,
			Header:  swHeaders(msgID, strconv.FormatInt(stale, 10), swSign([]byte(newSecret), msgID, stale, body)),
			RawBody: body,
			Keys:    []Key{oldKey, newKey},
			Now:     fixedNow,
		})
		if got.OK {
			t.Fatal("a stale delivery verified because a second key was configured")
		}
		if got.Reason != ReasonTimestampOutOfTolerance {
			t.Errorf("Reason = %q, want %q", got.Reason, ReasonTimestampOutOfTolerance)
		}
	})
}

func TestVerifyUnknownProfile(t *testing.T) {
	body := []byte(`{"action":"opened"}`)
	got := Verify(VerifyRequest{
		Profile: Profile("gitlab"),
		Header:  githubHeaders(githubSign(oldSecret, body), "pull_request", "d1"),
		RawBody: body,
		Keys:    []Key{{ID: "key_old", Secret: []byte(oldSecret)}},
		Now:     fixedNow,
	})
	if got.OK || got.Reason != ReasonUnknownProfile {
		t.Fatalf("got %+v, want rejection with %q", got, ReasonUnknownProfile)
	}
}

// TestRejectionReportsNothingDerived guards the rule that a failed
// verification leaks nothing read off the request: a caller must not be able to
// use a delivery id, an event type or a content key that no key vouched for.
func TestRejectionReportsNothingDerived(t *testing.T) {
	body := loadPayload(t, "github-pull-request-opened.json")
	got := Verify(VerifyRequest{
		Profile: ProfileGitHub,
		Header:  githubHeaders(githubSign("some-other-secret", body), "pull_request", "d-should-not-surface"),
		RawBody: body,
		Keys:    []Key{{ID: "key_old", Secret: []byte(oldSecret)}},
		Now:     fixedNow,
	})
	if got.OK {
		t.Fatal("a signature from an unconfigured secret verified")
	}
	if got.KeyID != "" || got.SourceEventID != "" || got.EventType != "" || got.Action != "" || got.ContentKey != "" {
		t.Fatalf("rejection carried derived fields: %+v", got)
	}
}

// TestDecodeSecret covers the ecosystem convention helper: base64, prefix
// optional.
func TestDecodeSecret(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		wantOK bool
	}{
		{name: "prefixed base64", in: "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw", wantOK: true},
		{name: "bare base64", in: "MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw", wantOK: true},
		{name: "prefix only", in: "whsec_", wantOK: false},
		{name: "empty", in: "", wantOK: false},
		{name: "not base64", in: "whsec_not base64 at all!!", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := DecodeSecret(tt.in)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && len(got) == 0 {
				t.Fatal("decoded to no bytes but reported success")
			}
		})
	}

	prefixed, ok := DecodeSecret("whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw")
	if !ok {
		t.Fatal("prefixed vector secret did not decode")
	}
	bare, ok := DecodeSecret("MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw")
	if !ok {
		t.Fatal("bare vector secret did not decode")
	}
	if string(prefixed) != string(bare) {
		t.Error("the prefix changed the decoded key")
	}
}

// TestUndecodableWhsecSecretIsAConfigurationFault: a "whsec_" value that is not
// base64 is not silently used as a literal key, and when it is the only key the
// verdict blames the configuration rather than the sender.
func TestUndecodableWhsecSecretIsAConfigurationFault(t *testing.T) {
	body := []byte(`{"ok":true}`)
	const msgID = "msg_bad_secret"
	ts := fixedNow.Unix()
	bad := "whsec_this is not base64 %%%"

	got := Verify(VerifyRequest{
		Profile: ProfileStandardWebhooks,
		Header:  swHeaders(msgID, strconv.FormatInt(ts, 10), swSign([]byte(bad), msgID, ts, body)),
		RawBody: body,
		Keys:    []Key{{ID: "key_bad", Secret: []byte(bad)}},
		Now:     fixedNow,
	})
	if got.OK {
		t.Fatal("an undecodable whsec_ secret was used as a literal key")
	}
	if got.Reason != ReasonNoEligibleKey {
		t.Errorf("Reason = %q, want %q", got.Reason, ReasonNoEligibleKey)
	}
}

// TestReasonsNeverLeakKeyMaterial is the §5 rule ("Neprozrazovat očekávaný
// podpis") as an assertion: sweep every rejection this package can produce and
// check that no Reason contains a secret, a key, an expected signature, or any
// encoding of them.
func TestReasonsNeverLeakKeyMaterial(t *testing.T) {
	body := loadPayload(t, "github-pull-request-synchronize.json")
	const msgID = "msg_leak_probe"
	ts := fixedNow.Unix()
	tsStr := strconv.FormatInt(ts, 10)

	swKey, ok := DecodeSecret("whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw")
	if !ok {
		t.Fatal("vector secret did not decode")
	}
	ghKeys := []Key{{ID: "key_old", Secret: []byte(oldSecret)}}
	swKeys := []Key{{ID: "key_sw", Secret: swKey}}

	requests := []VerifyRequest{
		// github: every rejecting shape.
		{Profile: ProfileGitHub, Header: githubHeaders("", "pull_request", "d1"), RawBody: body, Keys: ghKeys, Now: fixedNow},
		{Profile: ProfileGitHub, Header: githubHeaders(strings.TrimPrefix(githubSign(oldSecret, body), "sha256="), "pull_request", "d1"), RawBody: body, Keys: ghKeys, Now: fixedNow},
		{Profile: ProfileGitHub, Header: githubHeaders("sha256=zzzz", "pull_request", "d1"), RawBody: body, Keys: ghKeys, Now: fixedNow},
		{Profile: ProfileGitHub, Header: githubHeaders(githubSign("attacker-secret", body), "pull_request", "d1"), RawBody: body, Keys: ghKeys, Now: fixedNow},
		{Profile: ProfileGitHub, Header: githubHeaders(githubSign(oldSecret, []byte("not json")), "pull_request", "d1"), RawBody: []byte("not json"), Keys: ghKeys, Now: fixedNow},
		{Profile: ProfileGitHub, Header: githubHeaders(githubSign(oldSecret, body), "pull_request", "d1"), RawBody: body, Keys: nil, Now: fixedNow},
		{Profile: Profile("bitbucket"), Header: githubHeaders(githubSign(oldSecret, body), "pull_request", "d1"), RawBody: body, Keys: ghKeys, Now: fixedNow},
		// standard-webhooks: every rejecting shape.
		{Profile: ProfileStandardWebhooks, Header: swHeaders(msgID, tsStr, ""), RawBody: body, Keys: swKeys, Now: fixedNow},
		{Profile: ProfileStandardWebhooks, Header: swHeaders("", tsStr, swSign(swKey, msgID, ts, body)), RawBody: body, Keys: swKeys, Now: fixedNow},
		{Profile: ProfileStandardWebhooks, Header: swHeaders(msgID, "not-a-number", swSign(swKey, msgID, ts, body)), RawBody: body, Keys: swKeys, Now: fixedNow},
		{Profile: ProfileStandardWebhooks, Header: swHeaders(msgID, "0", swSign(swKey, msgID, 0, body)), RawBody: body, Keys: swKeys, Now: fixedNow},
		{Profile: ProfileStandardWebhooks, Header: swHeaders(msgID, tsStr, "v1a,AAAA"), RawBody: body, Keys: swKeys, Now: fixedNow},
		{Profile: ProfileStandardWebhooks, Header: swHeaders(msgID, tsStr, "v1,AAAA"), RawBody: body, Keys: swKeys, Now: fixedNow},
	}

	// Everything that must never appear in a Reason, in every encoding a
	// careless implementation might reach for.
	forbidden := []string{
		oldSecret,
		newSecret,
		string(swKey),
		"whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw",
		githubSign(oldSecret, body),
		strings.TrimPrefix(githubSign(oldSecret, body), "sha256="),
		swSign(swKey, msgID, ts, body),
		strings.TrimPrefix(swSign(swKey, msgID, ts, body), "v1,"),
		base64.StdEncoding.EncodeToString([]byte(oldSecret)),
		hex.EncodeToString([]byte(oldSecret)),
	}

	known := map[string]bool{
		ReasonUnknownProfile: true, ReasonTooManyKeys: true, ReasonNoEligibleKey: true,
		ReasonMissingSignatureHeader: true, ReasonMissingRequiredHeader: true,
		ReasonMalformedSignatureHeader: true, ReasonNoSupportedSignatureVersion: true,
		ReasonSignatureMismatch: true, ReasonMalformedTimestamp: true,
		ReasonTimestampOutOfTolerance: true, ReasonBodyNotJSON: true,
	}

	for i, req := range requests {
		got := Verify(req)
		if got.OK {
			t.Fatalf("request %d was expected to be rejected, got %+v", i, got)
		}
		if !known[got.Reason] {
			t.Errorf("request %d: Reason %q is not one of the documented constants", i, got.Reason)
		}
		for _, secret := range forbidden {
			if secret != "" && strings.Contains(got.Reason, secret) {
				t.Errorf("request %d: Reason leaked key material", i)
			}
		}
		// Belt and braces: a Reason is a short snake_case identifier. Anything
		// long or non-identifier-shaped is a sign something got interpolated.
		if len(got.Reason) > 40 || strings.ContainsAny(got.Reason, "=+/ .") {
			t.Errorf("request %d: Reason %q is not a bare identifier", i, got.Reason)
		}
	}
}
