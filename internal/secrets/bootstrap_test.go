package secrets

import (
	"context"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// silentLogger drops the "first-run generated"/"persisted" info lines
// so test output stays focused on actual failures.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// scrubEnv removes any test-environment values for the managed keys so
// each test starts from "neither env nor file set." t.Setenv handles
// per-test restoration; callers that want the env to *be* set call
// t.Setenv directly afterwards.
func scrubEnv(t *testing.T) {
	t.Helper()
	for _, m := range managed {
		t.Setenv(m.EnvVar, "")
	}
}

func TestLoadOrGenerate_FirstRun_GeneratesPersistsAndSetsEnv(t *testing.T) {
	scrubEnv(t)
	dir := t.TempDir()

	if err := LoadOrGenerate(context.Background(), dir, silentLogger()); err != nil {
		t.Fatalf("LoadOrGenerate first run: %v", err)
	}

	// Every managed secret must now be in the process env.
	for _, m := range managed {
		v := os.Getenv(m.EnvVar)
		if v == "" {
			t.Errorf("%s: expected env to be populated after first run, got empty", m.EnvVar)
			continue
		}
		// hex-encoded m.Bytes bytes ⇒ 2*m.Bytes chars
		if want := 2 * m.Bytes; len(v) != want {
			t.Errorf("%s: expected %d hex chars, got %d", m.EnvVar, want, len(v))
		}
	}

	// File must exist at 0600 with both keys.
	path := SecretsFilePath(dir)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat secrets file: %v", err)
	}
	if runtime.GOOS != "windows" {
		if got := info.Mode().Perm(); got != secretsFileMode {
			t.Errorf("secrets file mode: want %o, got %o", secretsFileMode, got)
		}
	}

	persisted, err := readFile(path)
	if err != nil {
		t.Fatalf("readFile: %v", err)
	}
	for _, m := range managed {
		if persisted[m.EnvVar] != os.Getenv(m.EnvVar) {
			t.Errorf("%s: env (%q) and file (%q) disagree", m.EnvVar, os.Getenv(m.EnvVar), persisted[m.EnvVar])
		}
	}
}

func TestLoadOrGenerate_SecondRun_ReadsFromFile(t *testing.T) {
	scrubEnv(t)
	dir := t.TempDir()
	ctx := context.Background()

	if err := LoadOrGenerate(ctx, dir, silentLogger()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	firstRunValues := map[string]string{}
	for _, m := range managed {
		firstRunValues[m.EnvVar] = os.Getenv(m.EnvVar)
	}

	// Simulate a fresh process — clear env, then call again. Persisted
	// file should drive the env back to the original values; no new
	// generation, no file rewrite.
	statBefore, err := os.Stat(SecretsFilePath(dir))
	if err != nil {
		t.Fatalf("stat before: %v", err)
	}

	scrubEnv(t)
	if err := LoadOrGenerate(ctx, dir, silentLogger()); err != nil {
		t.Fatalf("second run: %v", err)
	}

	for k, want := range firstRunValues {
		if got := os.Getenv(k); got != want {
			t.Errorf("%s: second-run env mismatch — want %q, got %q", k, want, got)
		}
	}

	statAfter, err := os.Stat(SecretsFilePath(dir))
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if !statBefore.ModTime().Equal(statAfter.ModTime()) {
		t.Errorf("secrets file was rewritten on second run (mtime changed) — expected no-op")
	}
}

func TestLoadOrGenerate_EnvVarWinsOverFile(t *testing.T) {
	scrubEnv(t)
	dir := t.TempDir()
	ctx := context.Background()

	// First run to plant a file with generated values.
	if err := LoadOrGenerate(ctx, dir, silentLogger()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	fileValue := os.Getenv("ENCRYPTION_KEY")
	if fileValue == "" {
		t.Fatal("setup: file value should be non-empty")
	}

	// Now set an explicit env value that's different and re-run. The
	// env value must survive — the file value is shadowed.
	const overrideKey = "deadbeef" + "deadbeef" + "deadbeef" + "deadbeef" +
		"deadbeef" + "deadbeef" + "deadbeef" + "deadbeef"
	t.Setenv("ENCRYPTION_KEY", overrideKey)

	if err := LoadOrGenerate(ctx, dir, silentLogger()); err != nil {
		t.Fatalf("override run: %v", err)
	}

	if got := os.Getenv("ENCRYPTION_KEY"); got != overrideKey {
		t.Errorf("env override lost: want %q, got %q", overrideKey, got)
	}
	// File on disk should still hold the original generated value —
	// LoadOrGenerate does not rewrite the file when env wins.
	persisted, err := readFile(SecretsFilePath(dir))
	if err != nil {
		t.Fatalf("readFile: %v", err)
	}
	if persisted["ENCRYPTION_KEY"] != fileValue {
		t.Errorf("env override clobbered file: file now has %q, expected unchanged %q", persisted["ENCRYPTION_KEY"], fileValue)
	}
}

func TestLoadOrGenerate_PartialFile_FillsMissingOnes(t *testing.T) {
	scrubEnv(t)
	dir := t.TempDir()
	ctx := context.Background()

	// Plant a file with only ENCRYPTION_KEY; NEXTAUTH_SECRET should
	// be generated and appended on the next run.
	plantedKey := strings.Repeat("ab", 32) // 64 hex chars
	if err := writeFile(ctx, SecretsFilePath(dir), map[string]string{"ENCRYPTION_KEY": plantedKey}); err != nil {
		t.Fatalf("plant file: %v", err)
	}

	if err := LoadOrGenerate(ctx, dir, silentLogger()); err != nil {
		t.Fatalf("LoadOrGenerate: %v", err)
	}

	if got := os.Getenv("ENCRYPTION_KEY"); got != plantedKey {
		t.Errorf("planted ENCRYPTION_KEY lost: want %q, got %q", plantedKey, got)
	}
	if got := os.Getenv("NEXTAUTH_SECRET"); got == "" {
		t.Errorf("NEXTAUTH_SECRET: expected generation, got empty env")
	}

	persisted, err := readFile(SecretsFilePath(dir))
	if err != nil {
		t.Fatalf("readFile: %v", err)
	}
	if persisted["NEXTAUTH_SECRET"] == "" {
		t.Errorf("NEXTAUTH_SECRET: file should now contain a generated value")
	}
	if persisted["ENCRYPTION_KEY"] != plantedKey {
		t.Errorf("planted ENCRYPTION_KEY rewritten: file now has %q, want %q", persisted["ENCRYPTION_KEY"], plantedKey)
	}
}

func TestLoadOrGenerate_EmptyDataDir_Errors(t *testing.T) {
	scrubEnv(t)
	if err := LoadOrGenerate(context.Background(), "", silentLogger()); err == nil {
		t.Error("expected error for empty dataDir, got nil")
	}
}

func TestLoadOrGenerate_PreservesUnknownKeysOnDisk(t *testing.T) {
	scrubEnv(t)
	dir := t.TempDir()
	ctx := context.Background()

	// Plant a file containing a key we don't manage. On the next
	// boot we should leave it alone — readFile keeps it, and since
	// no managed secret triggers a generation/write, the file is not
	// rewritten at all. But even if it were rewritten (e.g., a
	// missing managed key forces a regen), the unknown key must
	// round-trip back to disk.
	planted := map[string]string{
		"ENCRYPTION_KEY":            strings.Repeat("ab", 32),
		"SOME_CUSTOM_OPERATOR_FLAG": "yes",
	}
	if err := writeFile(ctx, SecretsFilePath(dir), planted); err != nil {
		t.Fatalf("plant: %v", err)
	}

	if err := LoadOrGenerate(ctx, dir, silentLogger()); err != nil {
		t.Fatalf("LoadOrGenerate: %v", err)
	}

	persisted, err := readFile(SecretsFilePath(dir))
	if err != nil {
		t.Fatalf("readFile: %v", err)
	}
	if persisted["SOME_CUSTOM_OPERATOR_FLAG"] != "yes" {
		t.Errorf("unknown key lost during rewrite: got %q", persisted["SOME_CUSTOM_OPERATOR_FLAG"])
	}
}

func TestLoadOrGenerate_RespectsCancelledContext(t *testing.T) {
	scrubEnv(t)
	dir := t.TempDir()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := LoadOrGenerate(ctx, dir, silentLogger())
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
	// The file must not exist — we cancelled before any generation
	// could persist.
	if _, statErr := os.Stat(SecretsFilePath(dir)); !os.IsNotExist(statErr) {
		t.Errorf("expected secrets file to not exist after cancelled bootstrap; stat err = %v", statErr)
	}
}

func TestWriteFile_AtomicReplace(t *testing.T) {
	// Ensure the rename leaves no stray .tmp siblings behind.
	dir := t.TempDir()
	path := filepath.Join(dir, secretsFileName)

	if err := writeFile(context.Background(), path, map[string]string{"A": "1"}); err != nil {
		t.Fatalf("writeFile: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.Name() == secretsFileName {
			continue
		}
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("stray temp file left behind: %s", e.Name())
		}
	}
}

func TestReadFile_ToleratesQuotedAndCommentedLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, secretsFileName)
	content := `# comment line
# another comment

NEXTAUTH_SECRET="quoted_value_with_underscores"
ENCRYPTION_KEY=bareValue
NOEQUALS_LINE
=leading_equal_is_ignored
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	got, err := readFile(path)
	if err != nil {
		t.Fatalf("readFile: %v", err)
	}
	if got["NEXTAUTH_SECRET"] != "quoted_value_with_underscores" {
		t.Errorf("quoted value: got %q", got["NEXTAUTH_SECRET"])
	}
	if got["ENCRYPTION_KEY"] != "bareValue" {
		t.Errorf("bare value: got %q", got["ENCRYPTION_KEY"])
	}
	if _, ok := got["NOEQUALS_LINE"]; ok {
		t.Errorf("no-equals line should have been skipped")
	}
}

func TestReadFile_MissingFileIsNotAnError(t *testing.T) {
	got, err := readFile(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("missing file should return (empty map, nil); got err %v", err)
	}
	if len(got) != 0 {
		t.Errorf("missing file should return empty map, got %d entries", len(got))
	}
}

func TestWriteFile_PermissionsOnDirAndFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX mode bits not meaningful on Windows")
	}
	// Verify writeFile produces a 0600 file even if the caller's umask
	// is permissive. CreateTemp inherits umask; we then explicitly
	// chmod the temp file before writing secrets, which is what
	// matters.
	oldUmask := syscallUmask(0o022)
	defer syscallUmask(oldUmask)

	dir := t.TempDir()
	path := filepath.Join(dir, secretsFileName)
	if err := writeFile(context.Background(), path, map[string]string{"K": "v"}); err != nil {
		t.Fatalf("writeFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != fs.FileMode(secretsFileMode) {
		t.Errorf("file mode: want %o, got %o", secretsFileMode, got)
	}
}
