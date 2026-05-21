// Package secrets owns the "crewship has never run here before" path
// for cryptographic secrets the server needs to start. The end-user
// install flow is `curl install.sh | bash` → `crewship start` → open
// the URL → web onboarding wizard; that flow falls apart the moment
// startup demands a human paste in 64 hex chars of entropy. So on a
// fresh data dir we generate ENCRYPTION_KEY and NEXTAUTH_SECRET
// ourselves, persist them under <dataDir>/secrets.env at mode 0600,
// and re-export them through os.Setenv so every existing call site
// (config.Load, encryption.getEncryptionKey, auth.NewJWTValidator,
// …) continues reading via os.Getenv with no diff at the read sites.
package secrets

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// managedSecret describes one auto-bootstrap secret: where it lives
// (env var name), how big a fresh value should be, and a Validate
// hook that catches truncated or hand-edited values BEFORE downstream
// callers blow up far away from the source.
//
// Validate is called against every value we accept — from env, from
// the persisted file, and even freshly generated ones (cheap insurance
// against generator bugs). It returns nil for "this value is usable as
// the env var promises," any other error for "regenerate or surface to
// the operator." LoadOrGenerate currently surfaces invalid persisted
// or env values rather than silently regenerating, on the principle
// that overwriting an operator-supplied secret without consent is
// worse than refusing to start.
type managedSecret struct {
	EnvVar   string
	Bytes    int
	Validate func(string) error
}

// managed is the canonical list of secrets we auto-bootstrap. Adding
// a new entry is the only step needed to extend coverage — readFile
// preserves unknown keys on disk and writeFile sorts deterministically.
//
// ENCRYPTION_KEY feeds aes.NewCipher in internal/encryption, which
// REQUIRES exactly 32 bytes (AES-256). The hex-encoded representation
// is what getEncryptionKey() already hex.DecodeStrings, so the on-disk
// form is 64 hex chars; the validator enforces that contract here
// rather than letting it fail mid-request in encryption.Encrypt().
//
// NEXTAUTH_SECRET feeds HKDF in internal/auth/jwt.go to derive three
// downstream keys (access, refresh, WS). Any random source >= 32
// bytes is cryptographically adequate; we enforce a 32-char minimum
// to match the doctor warning threshold so an under-strength value
// produces a consistent failure at every diagnostic surface.
var managed = []managedSecret{
	{
		EnvVar:   "ENCRYPTION_KEY",
		Bytes:    32,
		Validate: validateHex32,
	},
	{
		EnvVar:   "NEXTAUTH_SECRET",
		Bytes:    32,
		Validate: validateMinLen(32),
	},
	{
		// HMAC key for ADMIN-tier CLI tokens (Patch J). Separate from
		// ENCRYPTION_KEY so a DB dump alone cannot offline-crack admin
		// tokens — an attacker needs both the DB row and this env
		// value. Auto-generated on first boot and persisted to
		// secrets.env (mode 0600) like the others, so the ADMIN tier
		// is enabled out of the box; operators who deploy via systemd
		// EnvironmentFile / Kubernetes secrets can override the same
		// way they override ENCRYPTION_KEY.
		EnvVar:   "CREWSHIP_ADMIN_TOKEN_HMAC_KEY",
		Bytes:    32,
		Validate: validateHex32,
	},
}

func validateHex32(v string) error {
	b, err := hex.DecodeString(v)
	if err != nil {
		return fmt.Errorf("invalid hex encoding: %w", err)
	}
	if len(b) != 32 {
		return fmt.Errorf("expected 32 bytes after hex decode, got %d", len(b))
	}
	return nil
}

func validateMinLen(min int) func(string) error {
	return func(v string) error {
		if len(v) < min {
			return fmt.Errorf("expected at least %d characters, got %d", min, len(v))
		}
		return nil
	}
}

// ValidateNextAuthSecret exposes the same minimum-length check the
// bootstrap uses, so `crewship doctor` can confirm the persisted file
// holds a usable value rather than just confirming the file exists.
func ValidateNextAuthSecret(v string) error {
	return validateMinLen(32)(v)
}

// NextAuthSecretKey is the env-var name `LoadOrGenerate` writes the
// NEXTAUTH_SECRET value under. Exposed so doctor and other diagnostic
// surfaces can read the persisted file without hard-coding the literal.
const NextAuthSecretKey = "NEXTAUTH_SECRET"

// ReadPersisted parses the on-disk secrets file and returns its
// key→value map. Missing file returns (empty map, nil) — that's the
// "not yet bootstrapped" state. Exposed for diagnostic callers; the
// authoritative read path inside the package stays private.
func ReadPersisted(path string) (map[string]string, error) {
	return readFile(path)
}

const (
	secretsFileName = "secrets.env"
	secretsFileMode = 0o600
	secretsDirMode  = 0o700
)

// LoadOrGenerate populates the managed secrets for the running process,
// generating any that are missing and persisting them so subsequent
// boots stay deterministic.
//
// Resolution order per secret:
//
//  1. If the env var is already set non-empty, use it as-is. This
//     preserves deployments that inject secrets via systemd
//     EnvironmentFile, Kubernetes secret mount, Vault, etc.
//  2. Otherwise, if persisted in <dataDir>/secrets.env, load it and
//     re-export via os.Setenv so the downstream call sites (which
//     read via os.Getenv) work unchanged.
//  3. Otherwise, generate a cryptographically random value, persist
//     to <dataDir>/secrets.env atomically at mode 0600, and export
//     via os.Setenv.
//
// The function is idempotent across restarts: once secrets land on
// disk, subsequent boots take path (2) and emit no log lines.
//
// ctx is honoured at I/O boundaries (file open, write, rename, dir
// fsync) so a startup-orchestration cancel can take the bootstrap
// down cleanly rather than blocking on a hung filesystem.
func LoadOrGenerate(ctx context.Context, dataDir string, logger *slog.Logger) error {
	if dataDir == "" {
		return fmt.Errorf("secrets: dataDir is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	path := filepath.Join(dataDir, secretsFileName)

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("secrets bootstrap: %w", err)
	}

	persisted, err := readFile(path)
	if err != nil {
		return fmt.Errorf("secrets: read %s: %w", path, err)
	}

	generated := false
	for _, m := range managed {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("secrets bootstrap: %w", err)
		}

		// 1. Env var wins outright — but it has to actually be usable
		// downstream. Surfacing a clear "your ENCRYPTION_KEY isn't
		// valid AES-256 hex" beats letting encryption.Encrypt panic
		// twenty function calls later with no breadcrumb back.
		if v := os.Getenv(m.EnvVar); v != "" {
			if err := m.Validate(v); err != nil {
				return fmt.Errorf("secrets: env %s is invalid: %w", m.EnvVar, err)
			}
			continue
		}

		// 2. Persisted file — same validation. We don't auto-rewrite
		// a corrupt persisted value: doing so silently could erase a
		// secret the operator hand-edited, and any in-DB data
		// encrypted with the persisted key is already unrecoverable
		// at that point, so silent regeneration just papers over the
		// real problem.
		if v, ok := persisted[m.EnvVar]; ok && v != "" {
			if err := m.Validate(v); err != nil {
				return fmt.Errorf("secrets: persisted %s in secrets.env is invalid: %w (delete the entry to regenerate, or restore from backup)", m.EnvVar, err)
			}
			if err := os.Setenv(m.EnvVar, v); err != nil {
				return fmt.Errorf("secrets: setenv %s: %w", m.EnvVar, err)
			}
			continue
		}

		// 3. Generate fresh. Validate as cheap insurance against a
		// future bug in generateHex (e.g. someone changes Bytes to a
		// value that no longer satisfies the validator).
		v, err := generateHex(m.Bytes)
		if err != nil {
			return fmt.Errorf("secrets: generate %s: %w", m.EnvVar, err)
		}
		if err := m.Validate(v); err != nil {
			return fmt.Errorf("secrets: generated %s failed validation (generator bug): %w", m.EnvVar, err)
		}
		persisted[m.EnvVar] = v
		if err := os.Setenv(m.EnvVar, v); err != nil {
			return fmt.Errorf("secrets: setenv %s: %w", m.EnvVar, err)
		}
		generated = true
		logger.Info("first-run secret generated", "key", m.EnvVar, "bytes", m.Bytes)
	}

	if generated {
		if err := writeFile(ctx, path, persisted); err != nil {
			return fmt.Errorf("secrets: persist to %s: %w", path, err)
		}
		logger.Info("first-run secrets persisted", "path", path)
	}
	return nil
}

func generateHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("crypto/rand read: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// readFile parses a minimal env-file format (KEY=value, # comments,
// blank lines). It tolerates an absent file (returns an empty map)
// because that's the first-run case. Optional surrounding double
// quotes are stripped so a hand-edited file written with quoting still
// round-trips correctly.
//
// Every non-trivial return is wrapped with a short op label so the
// caller, which only knows "I couldn't load secrets", gets enough
// breadcrumbs to point at the failing step in production logs.
func readFile(path string) (map[string]string, error) {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.IndexByte(line, '=')
		if idx <= 0 {
			continue
		}
		k := strings.TrimSpace(line[:idx])
		v := strings.TrimSpace(line[idx+1:])
		v = strings.Trim(v, `"`)
		out[k] = v
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan %s: %w", path, err)
	}
	return out, nil
}

// writeFile persists secrets atomically. We write to a temp file in
// the same directory (so os.Rename is a rename, not a cross-fs copy),
// chmod 0600 BEFORE writing the secret bytes (so an interrupted write
// never leaves a world-readable file on disk), fsync the contents,
// rename over the destination, then fsync the parent directory so the
// rename itself is durable across a crash. The directory fsync is
// what most "atomic write" snippets get wrong — without it, a power
// loss after `os.Rename` can leave the temp file intact and the
// destination still pointing at the old inode.
func writeFile(ctx context.Context, path string, values map[string]string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("ctx: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, secretsDirMode); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, secretsFileName+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	// Best-effort cleanup if anything below the rename fails. After a
	// successful rename the temp path no longer exists, so the Remove
	// is harmless.
	defer os.Remove(tmpPath)

	if err := os.Chmod(tmpPath, secretsFileMode); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod %s: %w", tmpPath, err)
	}

	w := bufio.NewWriter(tmp)
	if _, err := fmt.Fprintln(w, "# crewship: auto-generated secrets — do not commit to source control"); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write header to %s: %w", tmpPath, err)
	}
	if _, err := fmt.Fprintln(w, "# Override any value by exporting the matching env var before `crewship start`."); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write header to %s: %w", tmpPath, err)
	}

	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, err := fmt.Fprintf(w, "%s=%s\n", k, values[k]); err != nil {
			_ = tmp.Close()
			return fmt.Errorf("write entry %q to %s: %w", k, tmpPath, err)
		}
	}
	if err := w.Flush(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("flush %s: %w", tmpPath, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("fsync %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpPath, err)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("ctx: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename %s → %s: %w", tmpPath, path, err)
	}
	return fsyncDir(dir)
}

// fsyncDir opens the directory read-only and calls Sync so the parent
// directory entry's rename is persisted across a power loss. Without
// this, an atomic os.Rename can be silently undone by a crash even
// though the data fsync above succeeded — POSIX permits the directory
// metadata change to remain in the page cache.
//
// Linux + the BSDs honour Sync on a directory fd. macOS does too
// (HFS+/APFS). Windows ignores it but that's safe; the Go runtime
// no-ops the call rather than erroring.
func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open dir %s: %w", dir, err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return fmt.Errorf("fsync dir %s: %w", dir, err)
	}
	return nil
}

// SecretsFilePath returns the absolute path where LoadOrGenerate
// persists auto-generated secrets for the given data dir. Exposed so
// `crewship doctor` can mention the file in its diagnostic output
// without re-deriving the join.
func SecretsFilePath(dataDir string) string {
	return filepath.Join(dataDir, secretsFileName)
}
