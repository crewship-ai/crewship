package main

// Coverage tests for the license-keygen CLI. The os.Exit error paths
// (unknown command, missing flags, bad key) cannot be unit-tested without
// a subprocess, so the tests focus on the success paths: keypair
// generation, license signing + signature verification, and the string
// helpers.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/license"
)

// captureStdout runs fn with os.Stdout redirected to a pipe and returns
// everything it printed.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()

	_ = w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	return string(out)
}

// withArgs swaps os.Args for the duration of fn.
func withArgs(t *testing.T, args []string, fn func()) {
	t.Helper()
	orig := os.Args
	os.Args = args
	defer func() { os.Args = orig }()
	fn()
}

var b64Pattern = regexp.MustCompile(`[A-Za-z0-9+/]{40,}={0,2}`)

func TestGenerateKeypair_PrintsWorkingKeys(t *testing.T) {
	out := captureStdout(t, generateKeypair)

	if !strings.Contains(out, "Public key") || !strings.Contains(out, "Private key") {
		t.Fatalf("output missing key sections: %q", out)
	}
	keys := b64Pattern.FindAllString(out, -1)
	if len(keys) < 2 {
		t.Fatalf("expected at least 2 base64 keys in output, got %d", len(keys))
	}
	pub, err := base64.StdEncoding.DecodeString(keys[0])
	if err != nil || len(pub) != ed25519.PublicKeySize {
		t.Fatalf("public key invalid: err=%v len=%d", err, len(pub))
	}
	priv, err := base64.StdEncoding.DecodeString(keys[1])
	if err != nil || len(priv) != ed25519.PrivateKeySize {
		t.Fatalf("private key invalid: err=%v len=%d", err, len(priv))
	}
	// The pair must actually work together.
	msg := []byte("crewship")
	sig := ed25519.Sign(ed25519.PrivateKey(priv), msg)
	if !ed25519.Verify(ed25519.PublicKey(pub), msg, sig) {
		t.Error("generated keypair does not verify its own signature")
	}
	// The ldflags example must embed the public key.
	if !strings.Contains(out, "internal/license.publicKey="+keys[0]) {
		t.Errorf("ldflags example missing public key: %q", out)
	}
}

func TestMain_GenerateKeypairCommand(t *testing.T) {
	var out string
	withArgs(t, []string{"license-keygen", "generate-keypair"}, func() {
		out = captureStdout(t, main)
	})
	if !strings.Contains(out, "Public key") {
		t.Errorf("main(generate-keypair) output: %q", out)
	}
}

func TestMain_SignCommand_WritesVerifiableLicense(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	outPath := filepath.Join(t.TempDir(), "license.json")

	args := []string{
		"license-keygen", "sign",
		"--key", base64.StdEncoding.EncodeToString(priv),
		"--id", "lic_test_1",
		"--name", "Acme Corp",
		"--org", "Acme Holdings",
		"--edition", "enterprise",
		"--max-crews", "7",
		"--max-agents", "3",
		"--max-members", "9",
		"--features", " sso, audit ,,  ",
		"--valid-days", "10",
		"--out", outPath,
	}
	var printed string
	withArgs(t, args, func() {
		printed = captureStdout(t, main)
	})
	if !strings.Contains(printed, "License written to "+outPath) {
		t.Errorf("missing confirmation output: %q", printed)
	}
	if !strings.Contains(printed, "lic_test_1") || !strings.Contains(printed, "enterprise") {
		t.Errorf("summary missing id/edition: %q", printed)
	}

	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read license: %v", err)
	}
	var signed struct {
		Payload   string `json:"payload"`
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal(raw, &signed); err != nil {
		t.Fatalf("unmarshal signed envelope: %v", err)
	}
	sig, err := base64.StdEncoding.DecodeString(signed.Signature)
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	if !ed25519.Verify(pub, []byte(signed.Payload), sig) {
		t.Fatal("signature does not verify against the payload")
	}

	var claims license.Claims
	if err := json.Unmarshal([]byte(signed.Payload), &claims); err != nil {
		t.Fatalf("unmarshal claims: %v", err)
	}
	if claims.LicenseID != "lic_test_1" || claims.LicenseeName != "Acme Corp" || claims.LicenseeOrg != "Acme Holdings" {
		t.Errorf("identity claims wrong: %+v", claims)
	}
	if claims.Edition != license.Edition("enterprise") {
		t.Errorf("edition = %q", claims.Edition)
	}
	if claims.MaxCrews != 7 || claims.MaxAgents != 3 || claims.MaxMembers != 9 {
		t.Errorf("limits wrong: %+v", claims)
	}
	if !reflect.DeepEqual(claims.Features, []string{"sso", "audit"}) {
		t.Errorf("features = %v, want [sso audit]", claims.Features)
	}
	wantExpiry := time.Now().Add(10 * 24 * time.Hour).Unix()
	if diff := claims.ExpiresAt - wantExpiry; diff < -60 || diff > 60 {
		t.Errorf("expiry %d not within a minute of now+10d (%d)", claims.ExpiresAt, wantExpiry)
	}
	if claims.IssuedAt <= 0 || claims.IssuedAt > claims.ExpiresAt {
		t.Errorf("issued_at %d inconsistent with expires_at %d", claims.IssuedAt, claims.ExpiresAt)
	}
	// 0600 file mode — the private signing material's output shouldn't be
	// world-readable.
	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("license file mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestSignLicense_NoFeaturesOmitsList(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	outPath := filepath.Join(t.TempDir(), "license.json")
	args := []string{
		"license-keygen", "sign",
		"--key", base64.StdEncoding.EncodeToString(priv),
		"--id", "lic_min",
		"--name", "Minimal",
		"--out", outPath,
	}
	withArgs(t, args, func() {
		_ = captureStdout(t, signLicense)
	})

	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var signed struct {
		Payload string `json:"payload"`
	}
	if err := json.Unmarshal(raw, &signed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var claims license.Claims
	if err := json.Unmarshal([]byte(signed.Payload), &claims); err != nil {
		t.Fatalf("claims: %v", err)
	}
	if claims.Features != nil {
		t.Errorf("features should be omitted, got %v", claims.Features)
	}
	// Defaults from the flag set.
	if claims.Edition != license.Edition("team") || claims.MaxCrews != 50 || claims.MaxAgents != 25 || claims.MaxMembers != 25 {
		t.Errorf("flag defaults wrong: %+v", claims)
	}
}

func TestSplitAndTrim(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"a,b,c", []string{"a", "b", "c"}},
		{" a , b ,, \t c ", []string{"a", "b", "c"}},
		{",,,", nil},
		{"single", []string{"single"}},
	}
	for _, c := range cases {
		if got := splitAndTrim(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("splitAndTrim(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestSplitString(t *testing.T) {
	if got := splitString("a,b", ','); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("splitString = %v", got)
	}
	if got := splitString("", ','); !reflect.DeepEqual(got, []string{""}) {
		t.Errorf("splitString empty = %v", got)
	}
	if got := splitString("no-sep", ','); !reflect.DeepEqual(got, []string{"no-sep"}) {
		t.Errorf("splitString no-sep = %v", got)
	}
}

func TestTrimString(t *testing.T) {
	cases := []struct{ in, want string }{
		{"  x  ", "x"},
		{"\tx\t", "x"},
		{"x", "x"},
		{"   ", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := trimString(c.in); got != c.want {
			t.Errorf("trimString(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
