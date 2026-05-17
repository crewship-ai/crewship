package encryption

import (
	"crypto/rand"
	"encoding/base64"
	"strings"
	"sync"
	"testing"
)

// extraTestKey is the same fixed 32-byte (256-bit) hex key used by the rest of
// the test suite. Centralising it here keeps key-source drift impossible while
// still avoiding any edit to the existing test files.
const extraTestKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// TestEncryption_RoundTripDeterminism_PlaintextStableAcrossDistinctCiphertexts
// pins the two-sided invariant:
//   - two calls to Encrypt with the same plaintext MUST produce different
//     ciphertexts (random IV — leaking nonce reuse would be catastrophic for
//     AES-GCM confidentiality + integrity)
//   - both ciphertexts MUST decrypt back to the exact same plaintext
//
// Existing tests check each half separately but never assert the full
// "different bytes, same plaintext" contract in one go. Regressions here would
// be silent (still round-trips, just nonce reuse) — exactly the kind of bug
// that doesn't surface in production until an attacker exploits it.
func TestEncryption_RoundTripDeterminism_PlaintextStableAcrossDistinctCiphertexts(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", extraTestKey)

	plaintext := "sk-ant-api03-rotation-canary"

	ct1, err := Encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt #1: %v", err)
	}
	ct2, err := Encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt #2: %v", err)
	}

	if ct1 == ct2 {
		t.Fatal("two encryptions of identical plaintext produced identical ciphertext — IV randomisation has broken (catastrophic GCM nonce reuse)")
	}

	pt1, err := Decrypt(ct1)
	if err != nil {
		t.Fatalf("decrypt #1: %v", err)
	}
	pt2, err := Decrypt(ct2)
	if err != nil {
		t.Fatalf("decrypt #2: %v", err)
	}

	if pt1 != plaintext || pt2 != plaintext {
		t.Fatalf("plaintext drift: pt1=%q pt2=%q want=%q", pt1, pt2, plaintext)
	}
}

// TestEncryption_TamperedCiphertext_DecryptRejectsWithError flips a single
// byte in the middle of a valid base64 payload (post IV+AuthTag region — i.e.
// inside the ciphertext bytes themselves) and asserts Decrypt returns an
// error and an empty plaintext. AES-GCM's authenticator MUST catch this; if
// it doesn't the credential store is silently returning garbage to callers.
// Existing tests only cover obviously invalid inputs ("v1:invalid-base64") —
// they don't catch a regression where Decrypt switches to a non-AEAD mode.
func TestEncryption_TamperedCiphertext_DecryptRejectsWithError(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", extraTestKey)

	plaintext := "tamper-canary-value-with-padding-to-have-room"
	ct, err := Encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if !strings.HasPrefix(ct, "v1:") {
		t.Fatalf("expected v1: prefix, got %q", ct)
	}

	blob, err := base64.StdEncoding.DecodeString(ct[3:])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(blob) < 36 {
		t.Fatalf("payload unexpectedly short: %d", len(blob))
	}

	// Flip a byte inside the ciphertext region (after IV[0:16] + AuthTag[16:32]).
	tampered := append([]byte{}, blob...)
	tampered[34] ^= 0x01

	got, err := Decrypt("v1:" + base64.StdEncoding.EncodeToString(tampered))
	if err == nil {
		t.Fatalf("decrypt of tampered ciphertext succeeded (returned %q) — AEAD authentication has broken", got)
	}
	if got != "" {
		t.Errorf("on auth failure plaintext must be empty; got %q", got)
	}
}

// TestEncryption_TamperedAuthTag_DecryptRejectsWithError covers the
// complementary failure: flip a byte inside the AuthTag (bytes 16..31).
// Some broken AEAD implementations validate ciphertext but not the tag —
// pin both.
func TestEncryption_TamperedAuthTag_DecryptRejectsWithError(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", extraTestKey)

	ct, err := Encrypt("tag-tamper-canary")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	blob, err := base64.StdEncoding.DecodeString(ct[3:])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}

	tampered := append([]byte{}, blob...)
	tampered[20] ^= 0xFF // mid-AuthTag flip

	if _, err := Decrypt("v1:" + base64.StdEncoding.EncodeToString(tampered)); err == nil {
		t.Fatal("decrypt accepted payload with tampered AuthTag — integrity check is broken")
	}
}

// TestEncryption_TamperedIV_DecryptRejectsWithError flips a byte in the IV
// region. Changing the nonce on a valid ciphertext must invalidate the tag.
// (This is technically redundant with the tag-tamper test but pins the
// boundary between IV bytes [0:16] and AuthTag bytes [16:32] — a slice
// off-by-one in Decrypt would land here.)
func TestEncryption_TamperedIV_DecryptRejectsWithError(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", extraTestKey)

	ct, err := Encrypt("iv-tamper-canary")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	blob, err := base64.StdEncoding.DecodeString(ct[3:])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}

	tampered := append([]byte{}, blob...)
	tampered[5] ^= 0x80 // mid-IV flip

	if _, err := Decrypt("v1:" + base64.StdEncoding.EncodeToString(tampered)); err == nil {
		t.Fatal("decrypt accepted payload with tampered IV — auth check is broken")
	}
}

// TestEncryption_TruncatedCiphertext_DecryptRejectsWithError lops the last 4
// bytes off a valid payload. After truncation the payload is still longer
// than the 32-byte minimum (so the explicit "too short" guard is NOT what
// fails), but the trailing ciphertext bytes are missing — GCM's tag
// verification must reject it. Distinct from the existing
// TestDecryptShortPayloadError which exercises the <32-byte guard.
func TestEncryption_TruncatedCiphertext_DecryptRejectsWithError(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", extraTestKey)

	// Long enough that after lopping 4 bytes we're still > 32 bytes.
	ct, err := Encrypt(strings.Repeat("X", 64))
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	blob, err := base64.StdEncoding.DecodeString(ct[3:])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(blob) <= 36 {
		t.Fatalf("payload too short to safely truncate: %d", len(blob))
	}

	truncated := blob[:len(blob)-4]
	if len(truncated) < 32 {
		t.Fatalf("after truncation payload would trip the <32 guard; need a longer plaintext")
	}

	if _, err := Decrypt("v1:" + base64.StdEncoding.EncodeToString(truncated)); err == nil {
		t.Fatal("decrypt accepted truncated ciphertext — GCM tag verification has broken")
	}
}

// TestEncryption_LargePlaintext_OneMegabyteRoundTrips exercises ciphertext at
// a size that surfaces buffer / copy / append bugs the small-string tests
// miss (e.g. a hidden int32 length cap). 1 MiB of cryptographically random
// bytes also defeats any accidental compression / dedup along the path. The
// existing TestEncryptDecryptLong only covers 2000 chars of repeated 'x'.
func TestEncryption_LargePlaintext_OneMegabyteRoundTrips(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", extraTestKey)

	const size = 1 << 20 // 1 MiB
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		t.Fatalf("rand: %v", err)
	}
	// Encrypt operates on strings — Go strings tolerate arbitrary bytes.
	plaintext := string(raw)

	ct, err := Encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt 1MiB: %v", err)
	}
	got, err := Decrypt(ct)
	if err != nil {
		t.Fatalf("decrypt 1MiB: %v", err)
	}
	if got != plaintext {
		t.Fatalf("1MiB round-trip mismatch (lengths got=%d want=%d)", len(got), len(plaintext))
	}
}

// TestEncryption_PlaintextWithNullBytes_RoundTripsByteForByte makes sure
// neither Encrypt nor Decrypt accidentally use C-style string handling that
// truncates at the first NUL. Credentials such as DER-encoded keys or random
// blobs can contain 0x00 in the middle.
func TestEncryption_PlaintextWithNullBytes_RoundTripsByteForByte(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", extraTestKey)

	plaintext := "before\x00after\x00\x00trailing"

	ct, err := Encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	got, err := Decrypt(ct)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if got != plaintext {
		t.Fatalf("null-byte plaintext drifted: got %q want %q", got, plaintext)
	}
	if len(got) != len(plaintext) {
		t.Fatalf("length mismatch: got %d want %d", len(got), len(plaintext))
	}
}

// TestEncryption_CrossPayloadIVSwap_DecryptRejectsWithError takes two valid
// ciphertexts encrypted under the SAME key and swaps their IVs. Both pieces
// are individually valid; the combination is not. GCM's tag is bound to the
// IV so mixing them must fail. Pins the property that an attacker capturing
// many ciphertexts can't recombine fragments to forge plaintext.
func TestEncryption_CrossPayloadIVSwap_DecryptRejectsWithError(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", extraTestKey)

	ctA, err := Encrypt("payload-A")
	if err != nil {
		t.Fatalf("encrypt A: %v", err)
	}
	ctB, err := Encrypt("payload-B")
	if err != nil {
		t.Fatalf("encrypt B: %v", err)
	}

	blobA, err := base64.StdEncoding.DecodeString(ctA[3:])
	if err != nil {
		t.Fatalf("decode A: %v", err)
	}
	blobB, err := base64.StdEncoding.DecodeString(ctB[3:])
	if err != nil {
		t.Fatalf("decode B: %v", err)
	}

	// Take IV from B, AuthTag+Ciphertext from A.
	hybrid := make([]byte, 0, len(blobA))
	hybrid = append(hybrid, blobB[:16]...) // IV from B
	hybrid = append(hybrid, blobA[16:]...) // AuthTag + Ciphertext from A

	if _, err := Decrypt("v1:" + base64.StdEncoding.EncodeToString(hybrid)); err == nil {
		t.Fatal("decrypt accepted payload with swapped IV across ciphertexts — GCM auth has broken")
	}
}

// TestEncryption_EmptyPlaintextTampered_DecryptRejectsWithError covers the
// pathological intersection: empty plaintext (the ciphertext region itself
// is zero bytes — only IV + AuthTag remain) AND a tampered AuthTag. The
// short-but-non-empty AuthTag still has to be verified. A naive
// length-zero-shortcut in Decrypt would skip the auth check.
func TestEncryption_EmptyPlaintextTampered_DecryptRejectsWithError(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", extraTestKey)

	ct, err := Encrypt("")
	if err != nil {
		t.Fatalf("encrypt empty: %v", err)
	}
	blob, err := base64.StdEncoding.DecodeString(ct[3:])
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(blob) != 32 {
		t.Fatalf("empty-plaintext payload must be exactly IV(16)+Tag(16)=32 bytes; got %d", len(blob))
	}

	// Flip a byte in the AuthTag region.
	tampered := append([]byte{}, blob...)
	tampered[24] ^= 0x55

	if _, err := Decrypt("v1:" + base64.StdEncoding.EncodeToString(tampered)); err == nil {
		t.Fatal("decrypt of empty-plaintext payload with tampered tag succeeded — auth check skipped on zero-length ciphertext")
	}
}

// TestEncryption_ConcurrentEncryptDecrypt_NoDataRaceOrCorruption fires N
// goroutines that each encrypt-then-decrypt a goroutine-specific plaintext
// in a tight loop. With -race this surfaces any unsynchronised access to
// the package-level aeadCache map. Without -race it still catches plaintext
// cross-contamination — a goroutine reading another goroutine's plaintext
// would be a credential leak between concurrent agent operations.
func TestEncryption_ConcurrentEncryptDecrypt_NoDataRaceOrCorruption(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", extraTestKey)

	const goroutines = 100
	const iterations = 20

	var wg sync.WaitGroup
	errCh := make(chan error, goroutines*iterations)

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			// Unique per-goroutine plaintext so any cross-contamination is
			// visible as a mismatched suffix.
			base := "concurrent-canary-" + strings.Repeat("X", id%17) + "-id"
			for i := 0; i < iterations; i++ {
				pt := base
				ct, err := Encrypt(pt)
				if err != nil {
					errCh <- err
					return
				}
				got, err := Decrypt(ct)
				if err != nil {
					errCh <- err
					return
				}
				if got != pt {
					errCh <- &mismatchErr{want: pt, got: got}
					return
				}
			}
		}(g)
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent round-trip failure: %v", err)
	}
}

// mismatchErr is a tiny error type so the concurrent test reports both sides
// without yanking in fmt allocations on the hot path.
type mismatchErr struct{ want, got string }

func (m *mismatchErr) Error() string {
	return "want=" + m.want + " got=" + m.got
}

// TestEncryption_ConcurrentDecryptOfSameCiphertext_AllReturnSamePlaintext
// hammers Decrypt on one shared ciphertext from many goroutines at once.
// AEAD is stateless after construction, so any divergence means the
// aeadCache fast path is serving the wrong AEAD or callers are sharing a
// mutable buffer. Distinct from the previous test which exercises Encrypt
// — the Decrypt hot path has its own race surface (sealed buffer reuse).
func TestEncryption_ConcurrentDecryptOfSameCiphertext_AllReturnSamePlaintext(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", extraTestKey)

	plaintext := "shared-secret-token-abcdef0123456789"
	ct, err := Encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt setup: %v", err)
	}

	const goroutines = 50
	const iterations = 50

	var wg sync.WaitGroup
	errCh := make(chan error, goroutines)

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				got, err := Decrypt(ct)
				if err != nil {
					errCh <- err
					return
				}
				if got != plaintext {
					errCh <- &mismatchErr{want: plaintext, got: got}
					return
				}
			}
		}()
	}

	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent decrypt failure: %v", err)
	}
}

// TestEncryption_SuccessiveRoundTrips_NoPlaintextDrift runs encrypt→decrypt
// in a chain for many iterations on the same plaintext, asserting it never
// drifts. Catches accumulators / shared buffers that mutate state across
// calls (the kind of bug that doesn't show in a single round-trip but
// surfaces after enough churn — e.g. an agent restarted hundreds of times).
func TestEncryption_SuccessiveRoundTrips_NoPlaintextDrift(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", extraTestKey)

	const iterations = 500
	plaintext := "stable-secret-must-not-drift"

	for i := 0; i < iterations; i++ {
		ct, err := Encrypt(plaintext)
		if err != nil {
			t.Fatalf("encrypt #%d: %v", i, err)
		}
		got, err := Decrypt(ct)
		if err != nil {
			t.Fatalf("decrypt #%d: %v", i, err)
		}
		if got != plaintext {
			t.Fatalf("drift at iteration %d: got %q want %q", i, got, plaintext)
		}
	}
}

// TestEncryption_NonceIsRandomAcrossCalls verifies the IV bytes themselves
// (not just the full ciphertext) differ between two encryptions of the same
// plaintext. A regression that resets the IV to a constant would still
// produce "different" ciphertexts only if the auth tag depended on a
// counter — pin the source of randomness directly. Existing
// TestEncryptDifferentOutputs compares the encoded strings, which can
// differ for spurious reasons (encoding flips) — this test goes one layer
// deeper.
func TestEncryption_NonceIsRandomAcrossCalls(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", extraTestKey)

	const trials = 5
	seenIVs := make(map[string]struct{}, trials)

	for i := 0; i < trials; i++ {
		ct, err := Encrypt("nonce-probe")
		if err != nil {
			t.Fatalf("encrypt #%d: %v", i, err)
		}
		blob, err := base64.StdEncoding.DecodeString(ct[3:])
		if err != nil {
			t.Fatalf("decode #%d: %v", i, err)
		}
		if len(blob) < 16 {
			t.Fatalf("payload too short: %d", len(blob))
		}
		iv := string(blob[:16])
		if _, dup := seenIVs[iv]; dup {
			t.Fatalf("IV collision at trial %d — IV generator is not random", i)
		}
		seenIVs[iv] = struct{}{}
	}
}

// TestEncryption_WrongKeyDecrypt_ReturnsErrorAndEmptyPlaintext is the more
// thorough sibling of the existing TestDecryptWrongKey. It additionally
// asserts that on auth failure the returned plaintext is empty — i.e.
// callers can't accidentally consume partial garbage. Empty-string return
// on error is the security contract for credential decryption.
func TestEncryption_WrongKeyDecrypt_ReturnsErrorAndEmptyPlaintext(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", extraTestKey)
	ct, err := Encrypt("wrong-key-canary-value")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}

	// Switch keys (same length, valid hex, different bytes).
	t.Setenv("ENCRYPTION_KEY", "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210")

	got, err := Decrypt(ct)
	if err == nil {
		t.Fatalf("decrypt with wrong key succeeded (returned %q) — must error", got)
	}
	if got != "" {
		t.Errorf("on auth failure plaintext MUST be empty; got %q (potential credential leak)", got)
	}
}
