package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/backup/convert"
)

const compatFixtureDir = "../../internal/backup/testdata/compat"

// TestBackupConvertAcceptance drives the built binary — not the RunE — on
// the committed v1 fixture, offline: CREWSHIP_SERVER points at a port
// nothing listens on and there is no login, so any network call fails the
// test. It pins the contract an operator (or agent) relies on: the report
// comes back as JSON under the global -f json, the converted bundle opens
// with the current reader, the original is untouched, and an existing
// --out is refused.
func TestBackupConvertAcceptance(t *testing.T) {
	bin := buildConversationBinary(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "v1.tar.zst")
	data, err := os.ReadFile(filepath.Join(compatFixtureDir, "v1-workspace.tar.zst"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(src, data, 0o600); err != nil {
		t.Fatal(err)
	}
	identity, err := filepath.Abs(filepath.Join(compatFixtureDir, "TEST-ONLY-age-identity.txt"))
	if err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		cmd := exec.Command(bin, args...)
		cmd.Env = append(os.Environ(),
			"HOME="+dir, "CREWSHIP_SERVER=http://127.0.0.1:1", "CREWSHIP_TOKEN=", "CREWSHIP_WORKSPACE=")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	out := filepath.Join(dir, "converted.tar.zst")
	stdout, err := run("backup", "convert", "--bundle", src, "--out", out, "--identity", identity, "-f", "json")
	if err != nil {
		t.Fatalf("convert failed: %v\n%s", err, stdout)
	}
	var rep convert.Report
	if err := json.Unmarshal([]byte(stdout), &rep); err != nil {
		t.Fatalf("-f json did not produce a JSON report: %v\n%s", err, stdout)
	}
	if rep.From != 1 || rep.To != backup.FormatVersion || rep.Output != out || len(rep.Unrecoverable) == 0 {
		t.Errorf("report = %+v", rep)
	}
	sum := sha256.Sum256(data)
	after, _ := os.ReadFile(src)
	afterSum := sha256.Sum256(after)
	if hex.EncodeToString(sum[:]) != hex.EncodeToString(afterSum[:]) {
		t.Error("the original bundle was modified")
	}
	m, err := backup.Inspect(t.Context(), out)
	if err != nil {
		t.Fatalf("current reader cannot open the converted bundle: %v", err)
	}
	if m.FormatVersion != backup.FormatVersion || m.Conversion == nil || m.Conversion.FromFormatVersion != 1 {
		t.Errorf("converted manifest: format v%d, conversion %+v", m.FormatVersion, m.Conversion)
	}

	// Human output: default path, same offline run.
	out2 := filepath.Join(dir, "human.tar.zst")
	text, err := run("backup", "convert", "--bundle", src, "--out", out2, "--identity", identity)
	if err != nil {
		t.Fatalf("convert (human) failed: %v\n%s", err, text)
	}
	for _, want := range []string{"v1 → v2", "cannot be recovered", "not modified"} {
		if !strings.Contains(text, want) {
			t.Errorf("human report lacks %q:\n%s", want, text)
		}
	}

	if msg, err := run("backup", "convert", "--bundle", src, "--out", out, "--identity", identity); err == nil || !strings.Contains(msg, "already exists") {
		t.Errorf("an existing --out must be refused, got err=%v\n%s", err, msg)
	}
	if msg, err := run("backup", "convert", "--bundle", src, "--out", filepath.Join(dir, "nokey.tar.zst")); err == nil || !strings.Contains(msg, "--identity") {
		t.Errorf("a missing key must say which flag to pass, got err=%v\n%s", err, msg)
	}
	if msg, err := run("backup", "convert", "--bundle", src, "--identity", identity, "--passphrase-file", identity); err == nil || !strings.Contains(msg, "only one") {
		t.Errorf("--identity with --passphrase-file must be refused, got err=%v\n%s", err, msg)
	}
}
