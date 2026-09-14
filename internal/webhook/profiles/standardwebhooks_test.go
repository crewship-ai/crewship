package profiles

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"
)

// swVectors mirrors testdata/standard-webhooks-vectors.json. The file holds the
// Standard Webhooks project's own hard-coded signing vectors, copied verbatim
// from its Go, Python and Rust reference libraries — see the _source fields in
// the file itself. None of these signatures was computed by Crewship, which is
// the point: they check our implementation against the standard rather than
// against itself.
type swVectors struct {
	Vectors []struct {
		Name            string `json:"name"`
		SourceFile      string `json:"_source_file"`
		Secret          string `json:"secret"`
		MsgID           string `json:"msg_id"`
		Timestamp       int64  `json:"timestamp"`
		Payload         string `json:"payload"`
		Signature       string `json:"signature"`
		WrongSignatures []struct {
			Value      string `json:"value"`
			SourceFile string `json:"_source_file"`
		} `json:"wrong_signatures"`
	} `json:"vectors"`
	MultiSignatureExample struct {
		Value string `json:"value"`
	} `json:"_multi_signature_example"`
}

func loadVectors(t *testing.T) swVectors {
	t.Helper()
	var v swVectors
	if err := json.Unmarshal(loadPayload(t, "standard-webhooks-vectors.json"), &v); err != nil {
		t.Fatalf("parse vectors: %v", err)
	}
	if len(v.Vectors) == 0 {
		t.Fatal("no vectors loaded")
	}
	return v
}

// TestStandardWebhooksOfficialVectors verifies the reference implementations'
// published signatures, with the secret supplied in both accepted shapes.
func TestStandardWebhooksOfficialVectors(t *testing.T) {
	for _, v := range loadVectors(t).Vectors {
		t.Run(v.Name, func(t *testing.T) {
			body := []byte(v.Payload)
			ts := strconv.FormatInt(v.Timestamp, 10)
			// Now is the instant the vector was signed, so the fixed
			// timestamps in the published vectors are inside tolerance.
			now := time.Unix(v.Timestamp, 0).UTC()

			decoded, ok := DecodeSecret(v.Secret)
			if !ok {
				t.Fatalf("vector secret %q did not decode", v.Name)
			}

			secretShapes := []struct {
				name   string
				secret []byte
			}{
				// The whsec_-prefixed ASCII, decoded by the profile itself.
				{name: "whsec_ prefixed", secret: []byte(v.Secret)},
				// The raw key bytes, as DecodeSecret produces them.
				{name: "raw key bytes", secret: decoded},
			}

			for _, shape := range secretShapes {
				t.Run(shape.name, func(t *testing.T) {
					got := Verify(VerifyRequest{
						Profile: ProfileStandardWebhooks,
						Header:  swHeaders(v.MsgID, ts, v.Signature),
						RawBody: body,
						Keys:    []Key{{ID: "key_sw", Secret: shape.secret}},
						Now:     now,
					})
					if !got.OK {
						t.Fatalf("official vector rejected: %q (source: %s)", got.Reason, v.SourceFile)
					}
					if got.KeyID != "key_sw" {
						t.Errorf("KeyID = %q, want key_sw", got.KeyID)
					}
					if got.SourceEventID != v.MsgID {
						t.Errorf("SourceEventID = %q, want %q", got.SourceEventID, v.MsgID)
					}
					if got.ContentKey != v.MsgID {
						t.Errorf("ContentKey = %q, want the signed message id %q", got.ContentKey, v.MsgID)
					}
					if got.EventType != "" || got.Action != "" {
						t.Errorf("EventType/Action = %q/%q, want empty: this profile carries neither", got.EventType, got.Action)
					}
				})
			}

			// The reference libraries' own known-bad signatures for this
			// message.
			for _, wrong := range v.WrongSignatures {
				t.Run("wrong signature", func(t *testing.T) {
					got := Verify(VerifyRequest{
						Profile: ProfileStandardWebhooks,
						Header:  swHeaders(v.MsgID, ts, wrong.Value),
						RawBody: body,
						Keys:    []Key{{ID: "key_sw", Secret: []byte(v.Secret)}},
						Now:     now,
					})
					if got.OK {
						t.Fatalf("accepted a known-bad signature (source: %s)", wrong.SourceFile)
					}
					if got.Reason != ReasonSignatureMismatch {
						t.Errorf("Reason = %q, want %q", got.Reason, ReasonSignatureMismatch)
					}
				})
			}

			// One byte of the signed body changed under the official
			// signature.
			t.Run("tampered body", func(t *testing.T) {
				tampered := []byte(v.Payload)
				tampered[len(tampered)-2]++
				got := Verify(VerifyRequest{
					Profile: ProfileStandardWebhooks,
					Header:  swHeaders(v.MsgID, ts, v.Signature),
					RawBody: tampered,
					Keys:    []Key{{ID: "key_sw", Secret: []byte(v.Secret)}},
					Now:     now,
				})
				if got.OK || got.Reason != ReasonSignatureMismatch {
					t.Fatalf("got %+v, want %q", got, ReasonSignatureMismatch)
				}
			})

			// The signed material binds the message id: reusing a valid
			// signature under a different id must fail.
			t.Run("swapped message id", func(t *testing.T) {
				got := Verify(VerifyRequest{
					Profile: ProfileStandardWebhooks,
					Header:  swHeaders(v.MsgID+"x", ts, v.Signature),
					RawBody: body,
					Keys:    []Key{{ID: "key_sw", Secret: []byte(v.Secret)}},
					Now:     now,
				})
				if got.OK || got.Reason != ReasonSignatureMismatch {
					t.Fatalf("got %+v, want %q", got, ReasonSignatureMismatch)
				}
			})
		})
	}
}

// TestStandardWebhooksTimestampBoundary is §11/T01's boundary case. The edge is
// inclusive, matching the reference implementation, which rejects only when the
// difference is strictly greater than the tolerance.
func TestStandardWebhooksTimestampBoundary(t *testing.T) {
	v := loadVectors(t).Vectors[0]
	body := []byte(v.Payload)
	signedAt := time.Unix(v.Timestamp, 0).UTC()

	tests := []struct {
		name       string
		now        time.Time
		wantOK     bool
		wantReason string
	}{
		{name: "same instant", now: signedAt, wantOK: true},
		{name: "5m in the past, exactly", now: signedAt.Add(Tolerance), wantOK: true},
		{name: "5m in the future, exactly", now: signedAt.Add(-Tolerance), wantOK: true},
		{name: "4m59s in the past", now: signedAt.Add(Tolerance - time.Second), wantOK: true},
		{name: "4m59s in the future", now: signedAt.Add(-Tolerance + time.Second), wantOK: true},
		{
			name:       "5m1s in the past",
			now:        signedAt.Add(Tolerance + time.Second),
			wantReason: ReasonTimestampOutOfTolerance,
		},
		{
			name:       "5m1s in the future",
			now:        signedAt.Add(-Tolerance - time.Second),
			wantReason: ReasonTimestampOutOfTolerance,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Verify(VerifyRequest{
				Profile: ProfileStandardWebhooks,
				Header:  swHeaders(v.MsgID, strconv.FormatInt(v.Timestamp, 10), v.Signature),
				RawBody: body,
				Keys:    []Key{{ID: "key_sw", Secret: []byte(v.Secret)}},
				Now:     tt.now,
			})
			if got.OK != tt.wantOK {
				t.Fatalf("OK = %v, want %v (reason %q)", got.OK, tt.wantOK, got.Reason)
			}
			if got.Reason != tt.wantReason {
				t.Errorf("Reason = %q, want %q", got.Reason, tt.wantReason)
			}
		})
	}
}

func TestStandardWebhooksRejections(t *testing.T) {
	v := loadVectors(t).Vectors[0]
	body := []byte(v.Payload)
	ts := strconv.FormatInt(v.Timestamp, 10)
	now := time.Unix(v.Timestamp, 0).UTC()
	bare := strings.TrimPrefix(v.Signature, "v1,")

	tests := []struct {
		name       string
		id         string
		timestamp  string
		signature  string
		wantReason string
	}{
		{
			name:       "missing signature header",
			id:         v.MsgID,
			timestamp:  ts,
			signature:  "",
			wantReason: ReasonMissingSignatureHeader,
		},
		{
			name:       "missing id",
			id:         "",
			timestamp:  ts,
			signature:  v.Signature,
			wantReason: ReasonMissingRequiredHeader,
		},
		{
			name:       "missing timestamp",
			id:         v.MsgID,
			timestamp:  "",
			signature:  v.Signature,
			wantReason: ReasonMissingRequiredHeader,
		},
		{
			name:       "timestamp is not a number",
			id:         v.MsgID,
			timestamp:  "yesterday",
			signature:  v.Signature,
			wantReason: ReasonMalformedTimestamp,
		},
		{
			name:       "timestamp is a float",
			id:         v.MsgID,
			timestamp:  ts + ".0",
			signature:  v.Signature,
			wantReason: ReasonMalformedTimestamp,
		},
		{
			name:       "timestamp overflows int64",
			id:         v.MsgID,
			timestamp:  "99999999999999999999",
			signature:  v.Signature,
			wantReason: ReasonMalformedTimestamp,
		},
		{
			name: "timestamp at MaxInt64 does not wrap back into the window",
			id:   v.MsgID,
			// The overflow guard internal/webhook.Handler.timestampFresh
			// documents: a nanosecond Duration cannot hold this, so a
			// time.Sub-based check could wrap it back inside tolerance.
			timestamp:  strconv.FormatInt(math.MaxInt64, 10),
			signature:  v.Signature,
			wantReason: ReasonTimestampOutOfTolerance,
		},
		{
			name:       "timestamp at MinInt64 does not wrap back into the window",
			id:         v.MsgID,
			timestamp:  strconv.FormatInt(math.MinInt64, 10),
			signature:  v.Signature,
			wantReason: ReasonTimestampOutOfTolerance,
		},
		{
			name:       "only an unimplemented version is offered",
			id:         v.MsgID,
			timestamp:  ts,
			signature:  "v1a,hnO3f9T8Ytu9HwrXslvumlUpqtNVqkhqw/enGzPCXe5BdqzCInXqYXFymVJaA7AZdpXwVLPo3mNl8EM+m7TBAg==",
			wantReason: ReasonNoSupportedSignatureVersion,
		},
		{
			name:       "several unimplemented versions",
			id:         v.MsgID,
			timestamp:  ts,
			signature:  "v1a,AAAA v2,BBBB v9,CCCC",
			wantReason: ReasonNoSupportedSignatureVersion,
		},
		{
			name:       "signature with no version at all",
			id:         v.MsgID,
			timestamp:  ts,
			signature:  bare,
			wantReason: ReasonNoSupportedSignatureVersion,
		},
		{
			name:       "bare v1 with an empty signature",
			id:         v.MsgID,
			timestamp:  ts,
			signature:  "v1,",
			wantReason: ReasonSignatureMismatch,
		},
		{
			name:       "truncated v1 signature",
			id:         v.MsgID,
			timestamp:  ts,
			signature:  "v1," + bare[:8],
			wantReason: ReasonSignatureMismatch,
		},
		{
			name:       "version prefix attached to the wrong scheme",
			id:         v.MsgID,
			timestamp:  ts,
			signature:  "v1a," + bare,
			wantReason: ReasonNoSupportedSignatureVersion,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Verify(VerifyRequest{
				Profile: ProfileStandardWebhooks,
				Header:  swHeaders(tt.id, tt.timestamp, tt.signature),
				RawBody: body,
				Keys:    []Key{{ID: "key_sw", Secret: []byte(v.Secret)}},
				Now:     now,
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

// TestStandardWebhooksSignatureList covers the space-separated list: any listed
// v1 may match, and versions we do not implement are skipped rather than fatal.
// The spec's own example header lists v1 next to v1a.
func TestStandardWebhooksSignatureList(t *testing.T) {
	fixture := loadVectors(t)
	v := fixture.Vectors[0]
	body := []byte(v.Payload)
	ts := strconv.FormatInt(v.Timestamp, 10)
	now := time.Unix(v.Timestamp, 0).UTC()
	junk := "v1,Ceo5qEr07ixe2NLpvHk3FH9bwy/WavXrAFQ/9tdO6mc="

	tests := []struct {
		name      string
		signature string
		wantOK    bool
	}{
		{
			name:      "valid signature last, after junk of both versions",
			signature: junk + " v2,Ceo5qEr07ixe2NLpvHk3FH9bwy/WavXrAFQ/9tdO6mc= " + v.Signature,
			wantOK:    true,
		},
		{
			name:      "valid signature first",
			signature: v.Signature + " " + junk,
			wantOK:    true,
		},
		{
			name:      "valid v1 beside an asymmetric v1a, the spec's own shape",
			signature: v.Signature + " v1a,hnO3f9T8Ytu9HwrXslvumlUpqtNVqkhqw/enGzPCXe5BdqzCInXqYXFymVJaA7AZdpXwVLPo3mNl8EM+m7TBAg==",
			wantOK:    true,
		},
		{
			name:      "extra whitespace between entries",
			signature: "  " + junk + "   " + v.Signature + "  ",
			wantOK:    true,
		},
		{
			name:      "only junk v1 entries",
			signature: junk + " " + junk,
			wantOK:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Verify(VerifyRequest{
				Profile: ProfileStandardWebhooks,
				Header:  swHeaders(v.MsgID, ts, tt.signature),
				RawBody: body,
				Keys:    []Key{{ID: "key_sw", Secret: []byte(v.Secret)}},
				Now:     now,
			})
			if got.OK != tt.wantOK {
				t.Fatalf("OK = %v, want %v (reason %q)", got.OK, tt.wantOK, got.Reason)
			}
		})
	}

	// The spec's published example header is used only for its shape: no
	// secret or payload accompanies it, so it can never verify. It must be
	// rejected as a mismatch, not as a parse failure.
	got := Verify(VerifyRequest{
		Profile: ProfileStandardWebhooks,
		Header:  swHeaders("msg_2KWPBgLlAfxdpx2AI54pPJ85f4W", "1674087231", fixture.MultiSignatureExample.Value),
		RawBody: body,
		Keys:    []Key{{ID: "key_sw", Secret: []byte(v.Secret)}},
		Now:     time.Unix(1674087231, 0).UTC(),
	})
	if got.OK {
		t.Fatal("the spec's illustrative header verified against our key")
	}
	if got.Reason != ReasonSignatureMismatch {
		t.Errorf("Reason = %q, want %q: the v1 entry parsed, it just did not match", got.Reason, ReasonSignatureMismatch)
	}
}

// TestStandardWebhooksTimestampNormalisation: the signed material uses the
// parsed integer, not the header text, exactly as the reference implementation
// formats timestamp.Unix().
func TestStandardWebhooksTimestampNormalisation(t *testing.T) {
	v := loadVectors(t).Vectors[0]
	got := Verify(VerifyRequest{
		Profile: ProfileStandardWebhooks,
		Header:  swHeaders(v.MsgID, "+"+strconv.FormatInt(v.Timestamp, 10), v.Signature),
		RawBody: []byte(v.Payload),
		Keys:    []Key{{ID: "key_sw", Secret: []byte(v.Secret)}},
		Now:     time.Unix(v.Timestamp, 0).UTC(),
	})
	if !got.OK {
		t.Fatalf("a signed-integer timestamp was rejected: %q", got.Reason)
	}
}
