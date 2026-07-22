package encryption

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// #1254 item C: secret encryption must fail CLOSED. Storing a secret in
// plaintext because nobody configured a key is a silent downgrade of the
// product's central at-rest guarantee; the only way to get the legacy
// behaviour is to ask for it out loud via CREWSHIP_ALLOW_PLAINTEXT_SECRETS.

// captureWarnings swaps the default slog logger for the duration of the test
// and returns a func yielding everything written to it.
func captureWarnings(t *testing.T) func() string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf.String
}

// clearKeyEnv removes every input that could make a key resolvable.
func clearKeyEnv(t *testing.T) {
	t.Helper()
	t.Setenv("ENCRYPTION_KEY", "")
	t.Setenv("ENCRYPTION_KEY_V2", "")
	t.Setenv(KeyVersionEnvVar, "")
	t.Setenv(AllowPlaintextSecretsEnvVar, "")
}

// RED #1: with no key and no opt-out, EncryptAtRest must refuse rather than
// hand the caller back the plaintext to store.
func TestEncryptAtRest_NoKey_NoOptOut_FailsClosed(t *testing.T) {
	clearKeyEnv(t)

	stored, encrypted, err := EncryptAtRest("whsec_plain")
	if err == nil {
		t.Fatalf("EncryptAtRest must fail closed with no key configured; got (%q, %v, nil)", stored, encrypted)
	}
	if stored != "" {
		t.Errorf("failed encrypt returned a storable value %q — the plaintext must never reach the caller", stored)
	}
	if encrypted {
		t.Error("encrypted=true on an error path")
	}
	if !strings.Contains(err.Error(), AllowPlaintextSecretsEnvVar) {
		t.Errorf("error must name the opt-out escape hatch, got: %v", err)
	}
	if !strings.Contains(err.Error(), "ENCRYPTION_KEY") {
		t.Errorf("error must name the key env var so the operator knows the fix, got: %v", err)
	}
	// #1254 item 1: callers (HTTP handlers) must be able to distinguish the
	// misconfiguration refusal from a genuine encrypt failure without string
	// matching, so they can surface an actionable message instead of a blind
	// 500. The refusal therefore wraps a stable sentinel.
	if !errors.Is(err, ErrPlaintextRefused) {
		t.Errorf("fail-closed refusal must wrap ErrPlaintextRefused (errors.Is), got: %v", err)
	}

	// An empty value stays a no-op — there is no secret to protect.
	if s, enc, err := EncryptAtRest(""); s != "" || enc || err != nil {
		t.Errorf("empty value: got (%q, %v, %v), want (\"\", false, nil)", s, enc, err)
	}
}

// RED #2: the opt-out restores the legacy plaintext path, but it must be loud
// — a Warn on EVERY write, not once per process.
func TestEncryptAtRest_NoKey_OptOut_PlaintextAndWarnsEveryTime(t *testing.T) {
	clearKeyEnv(t)
	t.Setenv(AllowPlaintextSecretsEnvVar, "true")
	logs := captureWarnings(t)

	for i := 0; i < 3; i++ {
		stored, encrypted, err := EncryptAtRest("whsec_plain")
		if err != nil {
			t.Fatalf("call %d: opt-out path must not error: %v", i, err)
		}
		if encrypted {
			t.Errorf("call %d: encrypted=true with no key", i)
		}
		if stored != "whsec_plain" {
			t.Errorf("call %d: stored = %q, want the plaintext unchanged", i, stored)
		}
	}

	out := logs()
	if n := strings.Count(out, "level=WARN"); n != 3 {
		t.Errorf("want one WARN per plaintext write (3), got %d\n%s", n, out)
	}
	if !strings.Contains(out, AllowPlaintextSecretsEnvVar) {
		t.Errorf("warning must name the opt-out var that produced it:\n%s", out)
	}
	if !strings.Contains(out, "ENCRYPTION_KEY") {
		t.Errorf("warning must name the fix:\n%s", out)
	}

	// The secret itself must never be logged.
	if strings.Contains(out, "whsec_plain") {
		t.Errorf("the plaintext secret leaked into the log:\n%s", out)
	}
}

// Truthy/falsy parsing of the opt-out. Anything that is not an explicit,
// recognised "yes" leaves the secure default in place.
func TestPlaintextSecretsAllowed_Parsing(t *testing.T) {
	cases := map[string]bool{
		"true": true, "TRUE": true, "True": true, "1": true,
		"yes": true, "on": true, " true ": true,
		"": false, "false": false, "0": false, "no": false,
		"maybe": false, "truthy": false,
	}
	for v, want := range cases {
		t.Run("v="+v, func(t *testing.T) {
			t.Setenv(AllowPlaintextSecretsEnvVar, v)
			if got := PlaintextSecretsAllowed(); got != want {
				t.Errorf("PlaintextSecretsAllowed() with %q = %v, want %v", v, got, want)
			}
		})
	}
}

// COMPATIBILITY (the single most important property of #1254 item C): values
// already stored in plaintext by the old fail-open path must keep resolving,
// whatever the flag says and whether or not a key is now configured. If this
// regresses, every existing key-less install's webhook secrets are bricked.
func TestDecryptIfEncrypted_LegacyPlaintextSurvivesFailClosed(t *testing.T) {
	const legacy = "9f2c1e7b4a6d8f0c3e5a7b9d1f3c5e7a9b1d3f5c7e9a1b3d5f7c9e1a3b5d7f90"

	t.Run("no key, no opt-out", func(t *testing.T) {
		clearKeyEnv(t)
		got, err := DecryptIfEncrypted(legacy)
		if err != nil || got != legacy {
			t.Fatalf("legacy plaintext read = (%q, %v), want (unchanged, nil)", got, err)
		}
	})

	t.Run("no key, opt-out on", func(t *testing.T) {
		clearKeyEnv(t)
		t.Setenv(AllowPlaintextSecretsEnvVar, "true")
		got, err := DecryptIfEncrypted(legacy)
		if err != nil || got != legacy {
			t.Fatalf("legacy plaintext read = (%q, %v), want (unchanged, nil)", got, err)
		}
	})

	t.Run("key now configured, fail-closed active", func(t *testing.T) {
		clearKeyEnv(t)
		t.Setenv("ENCRYPTION_KEY", extraTestKey)
		// Legacy plaintext still reads back untouched...
		got, err := DecryptIfEncrypted(legacy)
		if err != nil || got != legacy {
			t.Fatalf("legacy plaintext read = (%q, %v), want (unchanged, nil)", got, err)
		}
		// ...alongside newly-written envelopes in the same column.
		stored, encrypted, err := EncryptAtRest("whsec_new")
		if err != nil || !encrypted {
			t.Fatalf("EncryptAtRest with key = (%q, %v, %v)", stored, encrypted, err)
		}
		back, err := DecryptIfEncrypted(stored)
		if err != nil || back != "whsec_new" {
			t.Fatalf("envelope read = (%q, %v), want (whsec_new, nil)", back, err)
		}
	})
}
