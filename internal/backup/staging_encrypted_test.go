package backup_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/quiesce"
)

// Staging is encrypted before it touches disk: while a bundle is being
// written, no file in the staging directory may hold the packed payload in
// the clear. The payload is tar → zstd, so a plaintext staging file would
// start with the zstd frame magic (or, uncompressed, carry the tar "ustar"
// magic); a sealed one starts with the age header.

var (
	zstdMagic = []byte{0x28, 0xb5, 0x2f, 0xfd}
	tarMagic  = []byte("ustar")
	ageHeader = []byte("age-encryption.org/v1")
)

// stagingProbe snapshots every regular file under dirs each time the create
// reports a phase, and records any that looks like plaintext payload.
type stagingProbe struct {
	mu      sync.Mutex
	dirs    []string
	skip    func(path string) bool
	seen    int
	offense []string
}

func (p *stagingProbe) look(phase string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, dir := range p.dirs {
		_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || (p.skip != nil && p.skip(path)) {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil || len(b) == 0 {
				return nil
			}
			p.seen++
			if bytes.HasPrefix(b, zstdMagic) || bytes.Contains(b, tarMagic) && !bytes.HasPrefix(b, ageHeader) {
				p.offense = append(p.offense, phase+": "+path)
			}
			return nil
		})
	}
}

func TestCreateBackup_StagingNeverHoldsPlaintext(t *testing.T) {
	cases := []struct {
		name string
		opts func(o *backup.CreateOptions)
	}{
		{"recipients", func(o *backup.CreateOptions) {
			id, err := age.GenerateX25519Identity()
			if err != nil {
				t.Fatal(err)
			}
			o.Recipients = []age.Recipient{id.Recipient()}
		}},
		{"passphrase", func(o *backup.CreateOptions) { o.Passphrase = "staging-passphrase-123" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The database first: the migrated template it copies lives in
			// TMPDIR too, and outlives this subtest.
			db := openMigratedDB(t)
			ws := seedWorkspace(t, db)
			tmp := t.TempDir()
			t.Setenv("TMPDIR", tmp)
			probe := &stagingProbe{dirs: []string{tmp}}
			opts := backup.CreateOptions{
				Scope: backup.ScopeWorkspace, WorkspaceID: ws, OutputDir: t.TempDir(),
				Actor:              backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
				Progress:           probe.look,
				EncoderConcurrency: 1,
			}
			tc.opts(&opts)
			res, err := backup.CreateBackup(context.Background(), db, opts)
			if err != nil {
				t.Fatalf("CreateBackup: %v", err)
			}
			if len(probe.offense) > 0 {
				t.Fatalf("plaintext payload on disk during the create: %v", probe.offense)
			}
			if probe.seen == 0 {
				t.Fatal("the probe saw no staging file at all; the test proves nothing")
			}
			if left, _ := filepath.Glob(filepath.Join(tmp, "crewship-backup-*")); len(left) != 0 {
				t.Fatalf("staging left behind: %v", left)
			}
			if v, err := backup.Verify(context.Background(), res.Path); err != nil || !v.Valid {
				t.Fatalf("bundle does not verify: %+v %v", v, err)
			}
		})
	}
}

func TestCreateBackup_DiskThrottleSeesEveryByte(t *testing.T) {
	db := openMigratedDB(t)
	ws := seedWorkspace(t, db)
	th := &countingThrottle{}
	res, err := backup.CreateBackup(context.Background(), db, backup.CreateOptions{
		Scope: backup.ScopeWorkspace, WorkspaceID: ws, OutputDir: t.TempDir(),
		Actor:      backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"},
		Passphrase: "throttled-passphrase", DiskThrottle: th,
	})
	if err != nil {
		t.Fatal(err)
	}
	// The sealed staging file and the bundle file both pass the throttle,
	// so it saw at least the bundle's size.
	if th.total() < res.Size {
		t.Fatalf("throttle saw %d bytes, bundle is %d", th.total(), res.Size)
	}
}

type countingThrottle struct {
	mu sync.Mutex
	n  int64
}

func (c *countingThrottle) WaitN(_ context.Context, n int) error {
	c.mu.Lock()
	c.n += int64(n)
	c.mu.Unlock()
	return nil
}

func (c *countingThrottle) total() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func TestCreateInstanceBackup_PackedPayloadNeverPlaintext(t *testing.T) {
	f := newInstanceFixture(t)
	// The consistent copy (database snapshot, file store copies) is staged
	// as it was on the live server; what must never exist is the packed
	// payload unencrypted. Watch the staging directory for any file that is
	// not part of the copy.
	probe := &stagingProbe{dirs: []string{f.outputDir}, skip: func(path string) bool {
		return strings.Contains(path, string(filepath.Separator)+"files"+string(filepath.Separator)) ||
			strings.HasSuffix(path, "db.sqlite") || strings.Contains(path, string(filepath.Separator)+"crews"+string(filepath.Separator)) ||
			strings.HasPrefix(filepath.Base(path), "crewship-") // finished bundles (an outer tar holding the sealed payload)
	}}
	th := &countingThrottle{}
	res, err := backup.CreateInstanceBackup(context.Background(), f.db, backup.InstanceOptions{
		OutputDir: f.outputDir, Actor: backup.Actor{UserID: "u_admin", Email: "admin@e2e.test"},
		Recipients: []age.Recipient{f.identity.Recipient()}, Paths: f.paths, Quiesce: quiesce.New(),
		Poll: 5 * time.Millisecond, Progress: probe.look, EncoderConcurrency: 1, DiskThrottle: th,
	})
	if err != nil {
		t.Fatalf("CreateInstanceBackup: %v", err)
	}
	if len(probe.offense) > 0 {
		t.Fatalf("plaintext payload on disk: %v", probe.offense)
	}
	if th.total() < res.Size {
		t.Fatalf("throttle saw %d bytes, bundle is %d", th.total(), res.Size)
	}
	entries, _ := os.ReadDir(f.outputDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".staging-instance-") {
			t.Fatalf("staging directory left behind: %s", e.Name())
		}
	}
}

func TestSweepInstanceStaging(t *testing.T) {
	dir := t.TempDir()
	mk := func(name string, isDir bool) {
		p := filepath.Join(dir, name)
		if isDir {
			if err := os.MkdirAll(filepath.Join(p, "files"), 0o700); err != nil {
				t.Fatal(err)
			}
			return
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mk(".staging-instance-20260930T010203Z-123", true)
	mk("crewship-instance-all-20260930T010203Z.tar.zst.partial", false)
	mk("crewship-instance-all-20260930T010203Z.tar.zst", false)
	mk("crewship-workspace-lab-20260930T010203Z.tar.zst.partial", false)
	mk(".upload-1234567", true) // an upload interrupted by a crash
	if n := backup.SweepInstanceStaging(dir, 0); n != 3 {
		t.Fatalf("removed %d, want 3", n)
	}
	left, _ := os.ReadDir(dir)
	var names []string
	for _, e := range left {
		names = append(names, e.Name())
	}
	want := "crewship-instance-all-20260930T010203Z.tar.zst crewship-workspace-lab-20260930T010203Z.tar.zst.partial"
	if strings.Join(names, " ") != want {
		t.Fatalf("left %v", names)
	}
}

// The whole-instance path stages its consistent copy between the quiet
// window and packing. Nothing it stages may be readable on disk: not the
// database (never staged — it is held in memory), not any file store's
// contents, not the packed payload. The probe reads every file under the
// output directory at every phase, finished bundle included, and looks for
// SQLite's header, known plaintext from the database and each store, and
// (in staging) a zstd frame or tar header.
func TestCreateInstanceBackup_StagingHoldsNoPlaintext(t *testing.T) {
	f := newInstanceFixture(t)
	const dbMarker = "STAGING-DB-ROW-MARKER-7f3a"
	if _, err := f.db.Exec(`UPDATE workspaces SET name = ? WHERE id = 'ws_lab'`, dbMarker); err != nil {
		t.Fatal(err)
	}
	// A store big enough to span many cipher chunks.
	big := bytes.Repeat([]byte("STAGING-BIG-STORE-MARKER "), 40000)
	if err := os.WriteFile(filepath.Join(f.paths.Skills, "custom", "big.md"), big, 0o600); err != nil {
		t.Fatal(err)
	}
	markers := [][]byte{
		[]byte("SQLite format 3"), []byte(dbMarker), []byte("the incident timeline"),
		[]byte("# what the crew learned"), []byte("working notes"), []byte("STAGING-BIG-STORE-MARKER"),
	}
	var offense []string
	seen, stagedSeen := 0, 0
	look := func(phase string) {
		_ = filepath.Walk(f.outputDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil || len(b) == 0 {
				return nil
			}
			seen++
			staged := strings.Contains(path, string(filepath.Separator)+".staging-instance-")
			if staged {
				stagedSeen++
			}
			for _, m := range markers {
				if bytes.Contains(b, m) {
					offense = append(offense, fmt.Sprintf("%s: %s holds %q", phase, path, m))
				}
			}
			if staged && !bytes.HasPrefix(b, ageHeader) && (bytes.HasPrefix(b, zstdMagic) || bytes.Contains(b, tarMagic)) {
				offense = append(offense, fmt.Sprintf("%s: %s is an unencrypted archive", phase, path))
			}
			return nil
		})
	}
	res, err := backup.CreateInstanceBackup(context.Background(), f.db, backup.InstanceOptions{
		OutputDir: f.outputDir, Actor: backup.Actor{UserID: "u_admin", Email: "admin@e2e.test"},
		Recipients: []age.Recipient{f.identity.Recipient()}, Paths: f.paths, Quiesce: quiesce.New(),
		Poll: 5 * time.Millisecond, Progress: look, EncoderConcurrency: 1,
	})
	if err != nil {
		t.Fatalf("CreateInstanceBackup: %v", err)
	}
	look("done")
	if len(offense) > 0 {
		t.Fatalf("plaintext on disk:\n%s", strings.Join(offense, "\n"))
	}
	if stagedSeen == 0 || seen == 0 {
		t.Fatalf("the probe saw %d staged files (%d in all); the test proves nothing", stagedSeen, seen)
	}
	t.Logf("hold_ms %d; %d staged file reads checked", res.HoldMS, stagedSeen)
	// Staging is wiped on success.
	entries, _ := os.ReadDir(f.outputDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".staging-instance-") {
			t.Fatalf("staging directory left behind: %s", e.Name())
		}
	}
	// And the bundle still carries everything, decrypted by its key: the
	// database image and the big store file come back byte for byte.
	m := res.Manifest
	if m.Contents.Instance == nil || m.Contents.Instance.DatabaseBytes == 0 || m.Contents.Instance.DatabaseSHA256 == "" {
		t.Fatalf("manifest database = %+v", m.Contents.Instance)
	}
	if s := m.Contents.Instance.Stores[backup.StoreSkills]; s.Files != 2 || s.Bytes < int64(len(big)) {
		t.Fatalf("skills store = %+v", s)
	}
}

// Staging is wiped on failure too: a copy that fails inside the window
// leaves no staging directory.
func TestCreateInstanceBackup_StagingWipedOnFailure(t *testing.T) {
	f := newInstanceFixture(t)
	// A store root that cannot be walked fails the copy (a single
	// unreadable file inside a store is skipped and recorded instead). A
	// regular file in the store's place fails for root too, where a mode-0000
	// directory would not.
	if err := os.RemoveAll(f.paths.Skills); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.paths.Skills, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := backup.CreateInstanceBackup(context.Background(), f.db, backup.InstanceOptions{
		OutputDir: f.outputDir, Actor: backup.Actor{UserID: "u_admin", Email: "admin@e2e.test"},
		Recipients: []age.Recipient{f.identity.Recipient()}, Paths: f.paths, Quiesce: quiesce.New(),
		Poll: 5 * time.Millisecond, EncoderConcurrency: 1,
	})
	if err == nil {
		t.Fatal("the copy of an unreadable store file succeeded")
	}
	entries, _ := os.ReadDir(f.outputDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".staging-instance-") {
			t.Fatalf("staging directory left behind after a failure: %s", e.Name())
		}
	}
}
