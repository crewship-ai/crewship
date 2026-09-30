package backup_test

// Whole-instance backup and recovery, end to end: a seeded server with two
// workspaces, an attachment, a memory version blob and credentials sealed
// under two key versions is backed up into an instance bundle, then
// recovered offline into an empty data directory. Everything must come back,
// sessions must be gone, automations must be held, and the credentials must
// open with the recovery kit and nothing else.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/quiesce"
)

// Built, not literal, so secret scanners do not flag them.
var (
	instKeyV1 = strings.Repeat("a1", 32)
	instKeyV2 = strings.Repeat("b2", 32)
)

type instanceFixture struct {
	db        *sql.DB
	paths     backup.InstancePaths
	attSHA    string
	memSHA    string
	secrets   map[string]string // credential id → plaintext
	identity  *age.X25519Identity
	outputDir string
}

func newInstanceFixture(t *testing.T) *instanceFixture {
	t.Helper()
	ctx := context.Background()
	t.Setenv(encryption.KeyEnvVar("v1"), instKeyV1)
	t.Setenv(encryption.KeyEnvVar("v2"), instKeyV2)
	t.Setenv(encryption.KeyVersionEnvVar, "")

	db := openMigratedDB(t)
	const ws1 = "ws_e2e_1"
	for _, q := range []string{
		`INSERT INTO users (id, email, full_name) VALUES ('u_admin', 'admin@e2e.test', 'Admin')`,
		`INSERT INTO workspaces (id, name, slug) VALUES ('ws_e2e_1', 'E2E Workspace', 'e2e-ws')`,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('c_alpha', 'ws_e2e_1', 'Alpha Crew', 'alpha')`,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug, status) VALUES ('a_alice', 'c_alpha', 'ws_e2e_1', 'Alice', 'alice', 'IDLE')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	root := t.TempDir()
	f := &instanceFixture{
		db: db, secrets: map[string]string{}, outputDir: filepath.Join(root, "backups"),
		paths: backup.InstancePaths{
			Output: filepath.Join(root, "output"), Memory: filepath.Join(root, "memory"),
			Chats: filepath.Join(root, "chats"), Skills: filepath.Join(root, "skills"),
		},
	}
	// A second workspace, owned by someone else.
	for _, q := range []string{
		`INSERT INTO users (id, email, full_name) VALUES ('u_lab', 'lab@e2e.test', 'Lab')`,
		`INSERT INTO workspaces (id, name, slug) VALUES ('ws_lab', 'Lab', 'lab')`,
		`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('c_lab', 'ws_lab', 'Lab Crew', 'lab-crew')`,
		`INSERT INTO agents (id, crew_id, workspace_id, name, slug, status) VALUES ('a_lab', 'c_lab', 'ws_lab', 'Lab agent', 'lab-agent', 'IDLE')`,
		`INSERT INTO sessions (id, sessionToken, userId, expires) VALUES ('s1', 'tok-1', 'u_admin', '2099-01-01')`,
		`INSERT INTO user_sessions (id, user_id, expires_at) VALUES ('us1', 'u_lab', '2099-01-01')`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%v\n%s", err, q)
		}
	}
	// A chained journal in both workspaces, keyed by ENCRYPTION_KEY — the
	// drill verifies it with the key from the recovery kit.
	jw := journal.NewWriter(db, slog.New(slog.NewTextHandler(io.Discard, nil)), journal.WriterOptions{FlushInterval: time.Hour})
	for _, ws := range []string{ws1, "ws_lab", ws1} {
		if _, err := jw.Emit(ctx, journal.Entry{WorkspaceID: ws, Type: journal.EntryRunStarted, ActorType: journal.ActorAgent, Summary: "seeded"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := jw.Flush(ctx); err != nil {
		t.Fatal(err)
	}
	_ = jw.Close()
	f.attSHA = seedAttachment(t, db, f.paths.Output, ws1, "att_inst", "the incident timeline")

	// A memory version and its content-addressed blob.
	content := []byte("# what the crew learned\n")
	sum := sha256.Sum256(content)
	f.memSHA = hex.EncodeToString(sum[:])
	blob := filepath.Join(f.paths.Memory, "versions", f.memSHA[:2], f.memSHA)
	if err := os.MkdirAll(filepath.Dir(blob), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blob, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO memory_versions (id, workspace_id, path, tier, sha256, bytes, payload_ref)
		VALUES ('mv1', 'ws_lab', 'learned.md', 'learned', ?, ?, ?)`, f.memSHA, len(content), blob); err != nil {
		t.Fatal(err)
	}
	// Workspace files and skills: plain file stores.
	for p, body := range map[string]string{
		filepath.Join(f.paths.Output, "crews", "c_lab", "notes.md"): "working notes",
		filepath.Join(f.paths.Skills, "custom", "SKILL.md"):         "a skill",
	} {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// Credentials sealed under v1, then under v2 after a key rotation.
	seal := func(id, ws, plaintext string) {
		env, err := encryption.Encrypt(plaintext)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO credentials (id, workspace_id, name, type, status, encrypted_value, created_by)
			VALUES (?, ?, ?, 'SECRET', 'ACTIVE', ?, 'u_admin')`, id, ws, id, env); err != nil {
			t.Fatal(err)
		}
		f.secrets[id] = plaintext
	}
	seal("cred_v1", ws1, "first-generation-secret")
	t.Setenv(encryption.KeyVersionEnvVar, "v2")
	seal("cred_v2", "ws_lab", "second-generation-secret")

	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	f.identity = id
	return f
}

func (f *instanceFixture) create(t *testing.T, kit bool) *backup.InstanceResult {
	t.Helper()
	res, err := backup.CreateInstanceBackup(context.Background(), f.db, backup.InstanceOptions{
		OutputDir:   f.outputDir,
		Actor:       backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "instance_admin"},
		Recipients:  []age.Recipient{f.identity.Recipient()},
		Paths:       f.paths,
		RecoveryKit: kit,
		Quiesce:     quiesce.New(),
		Poll:        5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("CreateInstanceBackup: %v", err)
	}
	return res
}

func TestInstanceBackupRecoverRoundTrip(t *testing.T) {
	ctx := context.Background()
	f := newInstanceFixture(t)
	created := f.create(t, true)

	m := created.Manifest
	if m.Scope != backup.ScopeInstance || m.Contents.Instance == nil || !m.Encryption.Enabled {
		t.Fatalf("manifest = %+v", m)
	}
	inst := m.Contents.Instance
	if len(inst.Workspaces) != 2 || !inst.RecoveryKit || strings.Join(inst.KitVersions, ",") != "v1,v2" {
		t.Fatalf("instance contents = %+v", inst)
	}
	if inst.VaultEnvelopes["v1"] != 1 || inst.VaultEnvelopes["v2"] != 1 {
		t.Fatalf("vault envelopes = %v", inst.VaultEnvelopes)
	}
	if m.Contents.AttachmentsIncluded != 1 || m.Contents.MemoryBlobsIncluded != 1 || len(m.Contents.Incomplete) != 0 {
		t.Fatalf("contents: att=%d mem=%d incomplete=%+v", m.Contents.AttachmentsIncluded, m.Contents.MemoryBlobsIncluded, m.Contents.Incomplete)
	}
	if m.Contents.TableRowCounts["workspaces"] != 2 || m.Contents.TableRowCounts["credentials"] != 2 {
		t.Fatalf("row counts = %v", m.Contents.TableRowCounts)
	}
	if created.HoldMS < 0 || inst.HoldMS != created.HoldMS {
		t.Fatalf("hold_ms = %d / %d", created.HoldMS, inst.HoldMS)
	}
	if entries, _ := os.ReadDir(f.outputDir); len(entries) != 1 {
		t.Fatalf("output dir holds %d entries (staging left behind?)", len(entries))
	}

	dataDir := filepath.Join(t.TempDir(), "recovered")
	rep, err := backup.RecoverInstance(ctx, backup.RecoverOptions{
		BundlePath: created.Path, Identities: []age.Identity{f.identity}, DataDir: dataDir, Actor: "u_admin",
	})
	if err != nil {
		t.Fatalf("RecoverInstance: %v", err)
	}
	if rep.Result != backup.RestoreResultOK || len(rep.Incomplete) != 0 {
		t.Fatalf("result %s, incomplete %+v, warnings %v", rep.Result, rep.Incomplete, rep.Warnings)
	}
	if rep.SessionsDropped != 2 || !rep.AuthKeysRotated {
		t.Fatalf("sessions dropped %d, auth rotated %v", rep.SessionsDropped, rep.AuthKeysRotated)
	}

	// The files came back where a server booting from dataDir looks.
	for _, p := range []string{
		filepath.Join(dataDir, "output", "attachments", "ws_e2e_1", f.attSHA[:2], f.attSHA),
		filepath.Join(dataDir, "memory", "versions", f.memSHA[:2], f.memSHA),
		filepath.Join(dataDir, "output", "crews", "c_lab", "notes.md"),
		filepath.Join(dataDir, "skills", "custom", "SKILL.md"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("not restored: %s", p)
		}
	}

	restored, err := database.Open("file:" + rep.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	count := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := restored.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	if n := count(`SELECT COUNT(*) FROM workspaces`); n != 2 {
		t.Errorf("workspaces = %d", n)
	}
	if n := count(`SELECT COUNT(*) FROM sessions`) + count(`SELECT COUNT(*) FROM user_sessions`); n != 0 {
		t.Errorf("%d sessions survived the restore", n)
	}
	if n := count(`SELECT COUNT(*) FROM memory_versions WHERE payload_ref = ?`, filepath.Join(dataDir, "memory", "versions", f.memSHA[:2], f.memSHA)); n != 1 {
		t.Error("memory_versions.payload_ref still points at the source")
	}
	holds, err := quiesce.ReadHolds(ctx, restored)
	if err != nil {
		t.Fatal(err)
	}
	held := map[string]bool{}
	for _, h := range holds {
		held[h.Key] = true
	}
	if !held[quiesce.HoldRoutines] || !held[quiesce.HoldWebhooks] || !held[quiesce.HoldQueue] {
		t.Fatalf("holds = %+v", holds)
	}
	if n := count(`SELECT COUNT(*) FROM restore_reports WHERE kind = 'restore' AND result = 'ok'`); n != 1 {
		t.Errorf("restore report rows = %d", n)
	}

	// The keys file carries both key versions, 0600, and nothing else opens
	// the credentials.
	info, err := os.Stat(rep.KeysFile)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("keys file %s: %v %v", rep.KeysFile, err, info)
	}
	env, err := backup.ReadEnvFile(rep.KeysFile)
	if err != nil {
		t.Fatal(err)
	}
	if env[encryption.KeyEnvVar("v1")] != instKeyV1 || env[encryption.KeyEnvVar("v2")] != instKeyV2 {
		t.Fatalf("keys file does not carry both versions: %v", keysOnly(env))
	}
	secretsEnv, _ := backup.ReadEnvFile(rep.SecretsFile)
	if secretsEnv[encryption.KeyEnvVar("v1")] != instKeyV1 || len(secretsEnv[backup.NextAuthSecretEnv]) < 32 {
		t.Fatalf("secrets.env = %v", keysOnly(secretsEnv))
	}
	kitKeys := map[string][]byte{}
	for _, v := range []string{"v1", "v2"} {
		b, _ := hex.DecodeString(env[encryption.KeyEnvVar(v)])
		kitKeys[v] = b
	}
	wrong, _ := hex.DecodeString(strings.Repeat("cc", 32))
	rows, err := restored.QueryContext(ctx, `SELECT id, encrypted_value FROM credentials`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	opened := 0
	for rows.Next() {
		var id, env string
		if err := rows.Scan(&id, &env); err != nil {
			t.Fatal(err)
		}
		got, err := encryption.DecryptWithKeys(env, kitKeys)
		if err != nil || got != f.secrets[id] {
			t.Errorf("%s with the kit: %q %v", id, got, err)
		}
		if _, err := encryption.DecryptWithKeys(env, map[string][]byte{"v1": wrong, "v2": wrong}); err == nil {
			t.Errorf("%s opened without the kit", id)
		}
		opened++
	}
	if opened != 2 {
		t.Fatalf("restored %d credentials", opened)
	}
}

func keysOnly(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestInstanceRecoverWithoutKitIsPartialAndSaysWhy(t *testing.T) {
	f := newInstanceFixture(t)
	created := f.create(t, false)
	if created.Manifest.Contents.Instance.RecoveryKit {
		t.Fatal("kit off, bundle says it carries one")
	}
	dataDir := t.TempDir()
	rep, err := backup.RecoverInstance(context.Background(), backup.RecoverOptions{
		BundlePath: created.Path, Identities: []age.Identity{f.identity}, DataDir: filepath.Join(dataDir, "d"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Result != backup.RestoreResultPartial || rep.KeysFile != "" {
		t.Fatalf("result %s keys file %q", rep.Result, rep.KeysFile)
	}
	found := false
	for _, it := range rep.Incomplete {
		if it.Kind == backup.IncompleteCredentialLocked && it.Count == 2 {
			found = true
		}
	}
	if !found {
		t.Fatalf("no credential gap reported: %+v", rep.Incomplete)
	}
}

func TestInstanceRecoverRefusesAndGuards(t *testing.T) {
	f := newInstanceFixture(t)
	created := f.create(t, true)
	other, _ := age.GenerateX25519Identity()
	occupied := t.TempDir()
	if err := os.WriteFile(filepath.Join(occupied, "crewship.db"), []byte("live"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		opts    backup.RecoverOptions
		wantErr error
	}{
		{name: "non-empty data dir", opts: backup.RecoverOptions{BundlePath: created.Path, Identities: []age.Identity{f.identity}, DataDir: occupied}, wantErr: backup.ErrDataDirNotEmpty},
		{name: "wrong key", opts: backup.RecoverOptions{BundlePath: created.Path, Identities: []age.Identity{other}, DataDir: filepath.Join(t.TempDir(), "x")}, wantErr: backup.ErrDecryption},
		{name: "no key", opts: backup.RecoverOptions{BundlePath: created.Path, DataDir: filepath.Join(t.TempDir(), "y")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := backup.RecoverInstance(context.Background(), tc.opts)
			if err == nil {
				t.Fatal("recover went ahead")
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
	if b, _ := os.ReadFile(filepath.Join(occupied, "crewship.db")); string(b) != "live" {
		t.Fatal("a refused recover touched the occupied data dir")
	}
}

func TestInstanceDrillAndContentsCheck(t *testing.T) {
	ctx := context.Background()
	f := newInstanceFixture(t)
	created := f.create(t, true)

	check, err := backup.CheckBundleContents(ctx, created.Path, []age.Identity{f.identity}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !check.OK || check.ProofLevel != backup.ProofContents || check.Files == 0 || check.Tables == 0 {
		t.Fatalf("contents check = %+v", check)
	}

	drill, err := backup.DrillInstance(ctx, backup.DrillOptions{BundlePath: created.Path, Identities: []age.Identity{f.identity}, TempRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if drill.Result != backup.RestoreResultOK || drill.KeySource != "recovery kit" || drill.SHA256 != created.SHA256 {
		t.Fatalf("drill = %s (%s), failures %v, restore %+v", drill.Result, drill.KeySource, drill.Failures(), drill.Restore)
	}
	byName := map[string]backup.DrillCheck{}
	for _, c := range drill.Checks {
		byName[c.Name] = c
	}
	if c := byName[backup.DrillCheckCredentials]; c.Checked != 2 || c.Failed != 0 {
		t.Fatalf("credentials check = %+v", c)
	}
	if c := byName[backup.DrillCheckAttachments]; c.Checked != 1 || c.Failed != 0 {
		t.Fatalf("attachments check = %+v", c)
	}
	if c := byName[backup.DrillCheckJournal]; c.Status != backup.DrillStatusOK || c.Checked != 2 || !strings.Contains(c.Detail, "3 entries") {
		t.Fatalf("journal check = %+v", c)
	}

	// Without a kit the drill cannot unlock anything and says so: partial.
	noKit := f.create(t, false)
	drill, err = backup.DrillInstance(ctx, backup.DrillOptions{BundlePath: noKit.Path, Identities: []age.Identity{f.identity}, TempRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if drill.Result != backup.RestoreResultPartial {
		t.Fatalf("no-kit drill = %s", drill.Result)
	}
	// With this machine's keys allowed, the same bundle unlocks.
	drill, err = backup.DrillInstance(ctx, backup.DrillOptions{BundlePath: noKit.Path, Identities: []age.Identity{f.identity}, TempRoot: t.TempDir(), UseEnvKeys: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range drill.Checks {
		if c.Name == backup.DrillCheckCredentials && (c.Status != backup.DrillStatusOK || c.Checked != 2) {
			t.Fatalf("env-key credentials check = %+v", c)
		}
	}
}

func TestInstanceBackupSkipsWhileBusyAndReleasesTheHold(t *testing.T) {
	f := newInstanceFixture(t)
	ctrl := quiesce.New()
	_, err := backup.CreateInstanceBackup(context.Background(), f.db, backup.InstanceOptions{
		OutputDir: f.outputDir, Actor: backup.Actor{UserID: "u_admin"}, Recipients: []age.Recipient{f.identity.Recipient()},
		Paths: f.paths, Quiesce: ctrl, BusyWait: 30 * time.Millisecond, Poll: 5 * time.Millisecond,
		Busy: func(context.Context) (int, string, error) { return 1, "1 agent run", nil },
	})
	if !errors.Is(err, backup.ErrInstanceBusy) {
		t.Fatalf("err = %v, want ErrInstanceBusy", err)
	}
	if ctrl.Holding() {
		t.Fatal("a skipped backup left writes held")
	}
	// An unencrypted instance bundle is refused before anything happens.
	_, err = backup.CreateInstanceBackup(context.Background(), f.db, backup.InstanceOptions{
		OutputDir: f.outputDir, Actor: backup.Actor{UserID: "u_admin"}, Paths: f.paths, Quiesce: ctrl,
	})
	if !errors.Is(err, backup.ErrEncryptionRequired) {
		t.Fatalf("err = %v, want ErrEncryptionRequired", err)
	}
}

// The backup service drains and opens the quiet window itself, then hands it
// in: the copy runs inside it, the window is released before packing, and
// the phases are reported in order.
func TestInstanceBackupInsideACallersWindow(t *testing.T) {
	f := newInstanceFixture(t)
	ctrl := quiesce.New()
	w, err := ctrl.Begin(context.Background(), quiesce.Options{Poll: 5 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	var phases []string
	heldAtPack := true
	res, err := backup.CreateInstanceBackup(context.Background(), f.db, backup.InstanceOptions{
		OutputDir: f.outputDir, Actor: backup.Actor{UserID: "u_admin"}, Recipients: []age.Recipient{f.identity.Recipient()},
		Paths: f.paths, Window: w,
		Progress: func(p string) {
			phases = append(phases, p)
			if p == "pack" {
				heldAtPack = ctrl.Holding()
			}
		},
	})
	if err != nil {
		t.Fatalf("CreateInstanceBackup: %v", err)
	}
	if strings.Join(phases, ",") != "copy,pack,encrypt" {
		t.Fatalf("phases = %v", phases)
	}
	if heldAtPack || ctrl.Holding() {
		t.Fatal("writes were still held after the copy")
	}
	if res.HoldMS < 0 || res.Manifest.Scope != backup.ScopeInstance {
		t.Fatalf("result = hold %d scope %s", res.HoldMS, res.Manifest.Scope)
	}
}
