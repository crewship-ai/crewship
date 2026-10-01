package convert_test

// Historical fixture bundles: one per FormatVersion the current tooling
// promises to recover, restored into a fresh database on every run.
//
// The fixtures stand for bundles already sitting on operators' disks, so
// they are written once and never regenerated. `-update-compat-fixtures`
// only writes the ones that are MISSING — normally just the one for a
// freshly bumped FormatVersion — plus the TEST-ONLY keys on first use.
//
// Versions the current writer no longer produces (v1, v2) are built by
// reshaping what it produces today into the layout that version had, from
// git history of internal/backup: v1 dumped a fixed 10-table list and had
// no memory-blobs section; v2 dumped the FK-discovered set (the list at
// the v2→v3 boundary, a1ab934fb^) and gained memory-blobs late in its life;
// both filed /output under memory/<slug>/ and claimed memory_included
// without looking. Row columns are today's — that is what the fixture
// freezes, and future schema changes are then tested against these bytes.

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/klauspost/compress/zstd"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/backup/convert"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/memory"
	"github.com/crewship-ai/crewship/internal/testutil"
)

var updateCompatFixtures = flag.Bool("update-compat-fixtures", false,
	"write MISSING compat fixture bundles to internal/backup/testdata/compat (existing fixtures are never overwritten)")

const (
	compatDir       = "../testdata/compat"
	identityFile    = "TEST-ONLY-age-identity.txt"
	vaultKeyFile    = "TEST-ONLY-vault-key.hex"
	maxFixtureBytes = 200 << 10

	fxUser          = "u_compat"
	fxWorkspace     = "ws_compat"
	fxWorkspaceSlug = "compat-ws"
	fxCrew          = "c_compat"
	fxCrewSlug      = "compat"
	fxAgent         = "a_compat"
	fxAgentSlug     = "ada"
	fxIssue         = "m_compat"
	fxComment       = "mc_compat"
	fxJournal       = "j_compat"
	fxCredential    = "cred_compat"
	fxSecret        = "compat-fixture-secret-not-a-real-credential"
	fxMemoryPath    = "topics/compat.md"
	fxMemoryContent = "the crew remembers the compat fixture\n"
	fxOutputContent = "what /output held when memory/<slug>/ still meant /output\n"
	fxCrewMemory    = "crew charter written to /crew/shared/.memory\n"
	fxAgentMemory   = "ada's own memory note\n"
	fxCommentBody   = "compat fixture comment"
)

func fixturePath(v int) string {
	return filepath.Join(compatDir, fmt.Sprintf("v%d-workspace.tar.zst", v))
}

// what each fixture version carries, by construction. A restore must
// bring back exactly this; anything absent must be named by the converter.
type carries struct {
	issues, credentials, memoryVersions, crewMemory, output bool
}

func carriedBy(v int) carries {
	return carries{
		issues:         v >= 2,
		credentials:    v >= 2,
		memoryVersions: v >= 2,
		crewMemory:     v >= backup.FormatVersionCrewMemory,
		output:         v < backup.FormatVersionCrewMemory,
	}
}

// TestConverterChainCoversEveryVersion is the first half of the guard:
// bumping backup.FormatVersion without registering the step from the
// previous version fails here.
func TestConverterChainCoversEveryVersion(t *testing.T) {
	reg := convert.Default()
	for v := backup.OldestRecoverableFormatVersion; v < backup.FormatVersion; v++ {
		if _, ok := reg.Step(v); !ok {
			t.Errorf("no converter step v%d→v%d: bumping FormatVersion requires one in internal/backup/convert/steps.go", v, v+1)
		}
	}
	if _, ok := reg.Step(backup.FormatVersion); ok {
		t.Errorf("a step from v%d exists but FormatVersion is still %d: bump FormatVersion with it", backup.FormatVersion, backup.FormatVersion)
	}
	if backup.OldestRecoverableFormatVersion != 1 {
		t.Errorf("OldestRecoverableFormatVersion = %d; it must stay 1 — every bundle ever written stays recoverable", backup.OldestRecoverableFormatVersion)
	}
}

// TestCompatFixtures is the second half of the guard and the CI proof:
// every version from the oldest recoverable one to FormatVersion has a
// fixture, and each one restores — directly when the reader still reads
// it, and through the converter whenever it is older than current.
func TestCompatFixtures(t *testing.T) {
	if *updateCompatFixtures {
		writeMissingFixtures(t)
	}
	id := loadIdentity(t)
	t.Setenv("ENCRYPTION_KEY", loadVaultKey(t))
	t.Setenv(encryption.KeyVersionEnvVar, "")

	for v := backup.OldestRecoverableFormatVersion; v <= backup.FormatVersion; v++ {
		t.Run(fmt.Sprintf("v%d", v), func(t *testing.T) {
			src := fixturePath(v)
			info, err := os.Stat(src)
			if err != nil {
				t.Fatalf("no fixture for format v%d at %s: a FormatVersion bump needs one — run `go test ./internal/backup/convert -run TestCompatFixtures -update-compat-fixtures` and commit it", v, src)
			}
			if info.Size() > maxFixtureBytes {
				t.Errorf("fixture %s is %d bytes; keep fixtures under %d", src, info.Size(), maxFixtureBytes)
			}
			work := filepath.Join(t.TempDir(), filepath.Base(src))
			copyFile(t, src, work)
			if m := readManifest(t, work); m.FormatVersion != v {
				t.Fatalf("%s declares format_version %d", src, m.FormatVersion)
			}

			if backup.IsCompatible(v) {
				restoreAndCheck(t, work, v, id)
			} else {
				_, err := backup.RestoreBackup(context.Background(), openMigratedDB(t), restoreOpts(work, id, t.TempDir()))
				var cre *backup.ConvertRequiredError
				if !errors.As(err, &cre) || !strings.Contains(err.Error(), backup.ConvertCommand(work)) {
					t.Fatalf("restoring a v%d bundle outside the direct-read window must name the convert command, got %v", v, err)
				}
			}

			if v == backup.FormatVersion {
				return
			}
			out := filepath.Join(t.TempDir(), "converted.tar.zst")
			rep, err := convert.Convert(context.Background(), convert.Options{
				BundlePath: work, OutPath: out, Identities: []age.Identity{id},
			})
			if err != nil {
				t.Fatalf("convert v%d: %v", v, err)
			}
			if got := fileDigest(t, work); got != fileDigest(t, src) {
				t.Fatal("conversion modified the source bundle")
			}
			checkReport(t, v, rep)
			restoreAndCheck(t, out, v, id)
		})
	}
}

func checkReport(t *testing.T, v int, rep *convert.Report) {
	t.Helper()
	c := carriedBy(v)
	all := strings.Join(rep.Unrecoverable, "\n")
	if !c.crewMemory && !strings.Contains(all, "/crew") {
		t.Errorf("v%d never carried /crew memory and the report does not say so:\n%s", v, all)
	}
	for what, has := range map[string]bool{"issues": c.issues, "credentials": c.credentials, "memory versions": c.memoryVersions} {
		if !has && !strings.Contains(all, what) {
			t.Errorf("v%d never carried %s and the report does not say so:\n%s", v, what, all)
		}
		if has && strings.Contains(all, what) {
			t.Errorf("v%d carries %s but the report calls them unrecoverable:\n%s", v, what, all)
		}
	}
	if len(rep.Steps) != backup.FormatVersion-v {
		t.Errorf("report has %d steps, want %d", len(rep.Steps), backup.FormatVersion-v)
	}
}

func restoreOpts(path string, id age.Identity, blobRoot string) backup.RestoreOptions {
	return backup.RestoreOptions{
		Path:       path,
		Identities: []age.Identity{id},
		Actor:      backup.Actor{UserID: fxUser, Email: "admin@compat.test", Role: "ADMIN"},
		BlobRoot:   blobRoot,
	}
}

func restoreAndCheck(t *testing.T, path string, v int, id age.Identity) {
	t.Helper()
	ctx := context.Background()
	c := carriedBy(v)
	target := openMigratedDB(t)
	blobRoot := filepath.Join(t.TempDir(), "versions")
	res, err := backup.RestoreBackup(ctx, target, restoreOpts(path, id, blobRoot))
	if err != nil {
		t.Fatalf("restore %s: %v", filepath.Base(path), err)
	}
	if res.RestoredWorkspaceID != "" && res.RestoredWorkspaceID != fxWorkspace {
		t.Errorf("restored under workspace %q, want %q", res.RestoredWorkspaceID, fxWorkspace)
	}

	one := func(what, query string, args ...any) string {
		t.Helper()
		var s string
		if err := target.QueryRowContext(ctx, query, args...).Scan(&s); err != nil {
			t.Errorf("%s: %v", what, err)
		}
		return s
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := target.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return n
	}

	if got := one("workspace", `SELECT slug FROM workspaces WHERE id = ?`, fxWorkspace); got != fxWorkspaceSlug {
		t.Errorf("workspace slug = %q", got)
	}
	if got := one("crew", `SELECT slug FROM crews WHERE id = ? AND workspace_id = ?`, fxCrew, fxWorkspace); got != fxCrewSlug {
		t.Errorf("crew slug = %q", got)
	}
	if got := one("agent", `SELECT slug FROM agents WHERE id = ? AND crew_id = ?`, fxAgent, fxCrew); got != fxAgentSlug {
		t.Errorf("agent slug = %q", got)
	}
	if n := count(`SELECT COUNT(*) FROM journal_entries WHERE id = ? AND workspace_id = ?`, fxJournal, fxWorkspace); n != 1 {
		t.Errorf("journal entry rows = %d, want 1", n)
	}

	issues := count(`SELECT COUNT(*) FROM missions WHERE id = ?`, fxIssue)
	comments := count(`SELECT COUNT(*) FROM mission_comments WHERE id = ? AND mission_id = ? AND body = ?`, fxComment, fxIssue, fxCommentBody)
	if c.issues && (issues != 1 || comments != 1) {
		t.Errorf("issue rows = %d, comment rows = %d; want 1 and 1", issues, comments)
	}
	if !c.issues && (issues != 0 || comments != 0) {
		t.Errorf("a v%d bundle cannot carry issues, yet %d issue(s) and %d comment(s) appeared", v, issues, comments)
	}

	creds := count(`SELECT COUNT(*) FROM credentials WHERE id = ?`, fxCredential)
	if c.credentials {
		sealed := one("credential", `SELECT encrypted_value FROM credentials WHERE id = ?`, fxCredential)
		plain, err := encryption.Decrypt(sealed)
		if err != nil || plain != fxSecret {
			t.Errorf("credential does not open with the fixture vault key: %q, %v", plain, err)
		}
	} else if creds != 0 {
		t.Errorf("a v%d bundle cannot carry credentials, yet %d appeared", v, creds)
	}

	sum := sha256.Sum256([]byte(fxMemoryContent))
	sha := hex.EncodeToString(sum[:])
	if c.memoryVersions {
		got, err := memory.ReadVersion(ctx, target, fxWorkspace, fxMemoryPath, sha)
		if err != nil || string(got) != fxMemoryContent {
			t.Errorf("memory version not readable after restore: %q, %v", got, err)
		}
	} else if n := count(`SELECT COUNT(*) FROM memory_versions WHERE workspace_id = ?`, fxWorkspace); n != 0 {
		t.Errorf("a v%d bundle cannot carry memory versions, yet %d appeared", v, n)
	}

	// Crew filesystem sections land through docker, which a unit test
	// does not have; prove they are in the bundle, readable by the same
	// extractor the docker phase streams from.
	files := crewSectionFiles(t, path, id)
	if c.crewMemory {
		if files["crew/shared/.memory/CREW.md"] != fxCrewMemory || files["crew/agents/ada/.memory/AGENT.md"] != fxAgentMemory {
			t.Errorf("crew memory files missing from the bundle: %v", keys(files))
		}
	}
	if c.output && files["memory/notes.md"] != fxOutputContent {
		t.Errorf("the /output section is missing from the bundle: %v", keys(files))
	}
	m := readManifest(t, path)
	for _, cs := range m.Contents.Crews {
		if cs.Slug != fxCrewSlug {
			continue
		}
		if cs.HasCrewMemory(m.FormatVersion) != c.crewMemory {
			t.Errorf("manifest says crew memory %t, bundle carries %t", cs.HasCrewMemory(m.FormatVersion), c.crewMemory)
		}
		if !cs.HasFilesystemSections(m.FormatVersion) {
			t.Error("the crew's filesystem sections would be skipped by the docker phase")
		}
	}
}

// crewSectionFiles returns the regular files of the fixture crew's crew/
// and memory/ sections, keyed "<section>/<path inside the section>".
func crewSectionFiles(t *testing.T, path string, id age.Identity) map[string]string {
	t.Helper()
	ctx := context.Background()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	_, sealed, err := backup.ReadBundle(f)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := backup.DecryptStream(sealed, id)
	if err != nil {
		t.Fatal(err)
	}
	ex, err := backup.ExtractPayload(ctx, plain)
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()
	out := map[string]string{}
	for section, open := range map[string]func(context.Context, string) (io.ReadCloser, bool, error){
		"crew": ex.OpenCrew, "memory": ex.OpenMemory,
	} {
		rc, ok, err := open(ctx, fxCrewSlug)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			continue
		}
		tr := tar.NewReader(rc)
		for {
			hdr, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if hdr.Typeflag == tar.TypeReg {
				b, _ := io.ReadAll(tr)
				out[section+"/"+strings.TrimPrefix(hdr.Name, "./")] = string(b)
			}
		}
		_ = rc.Close()
	}
	return out
}

// ---------------------------------------------------------------------
// Fixture generation (only with -update-compat-fixtures)

func writeMissingFixtures(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(compatDir, 0o755); err != nil {
		t.Fatal(err)
	}
	idPath := filepath.Join(compatDir, identityFile)
	if _, err := os.Stat(idPath); os.IsNotExist(err) {
		id, err := age.GenerateX25519Identity()
		if err != nil {
			t.Fatal(err)
		}
		body := "# TEST-ONLY age identity for the compat fixture bundles in this directory.\n" +
			"# It protects nothing: it is committed so CI can decrypt the fixtures.\n" +
			"# Never use it for a real backup.\n" +
			"# public key: " + id.Recipient().String() + "\n" + id.String() + "\n"
		if err := os.WriteFile(idPath, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	vkPath := filepath.Join(compatDir, vaultKeyFile)
	if _, err := os.Stat(vkPath); os.IsNotExist(err) {
		sum := sha256.Sum256([]byte("crewship compat fixture vault key - TEST ONLY - protects nothing"))
		body := "# TEST-ONLY vault key (ENCRYPTION_KEY) the fixture credentials are sealed with. Protects nothing.\n" +
			hex.EncodeToString(sum[:]) + "\n"
		if err := os.WriteFile(vkPath, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	id := loadIdentity(t)
	t.Setenv("ENCRYPTION_KEY", loadVaultKey(t))
	t.Setenv(encryption.KeyVersionEnvVar, "")
	for v := backup.OldestRecoverableFormatVersion; v <= backup.FormatVersion; v++ {
		p := fixturePath(v)
		if _, err := os.Stat(p); err == nil {
			continue // never regenerate: existing fixtures stand for bundles in the field
		}
		data := buildFixture(t, v, id)
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d bytes)", p, len(data))
	}
}

type payloadEntry struct {
	hdr  tar.Header
	body []byte
}

func buildFixture(t *testing.T, v int, id *age.X25519Identity) []byte {
	t.Helper()
	ctx := context.Background()
	source := openMigratedDB(t)
	blobRoot := filepath.Join(t.TempDir(), "versions")
	seedCompat(t, source, blobRoot)

	res, err := backup.CreateBackup(ctx, source, backup.CreateOptions{
		Scope:       backup.ScopeWorkspace,
		WorkspaceID: fxWorkspace,
		OutputDir:   t.TempDir(),
		Actor:       backup.Actor{UserID: fxUser, Email: "admin@compat.test", Role: "ADMIN"},
		Recipients:  []age.Recipient{id.Recipient()},
		BlobRoot:    blobRoot,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	m, entries := openPayload(t, res.Path, id)

	switch {
	case v == backup.FormatVersion:
		currentLayout(m, &entries)
	case v == 1:
		historicalLayout(t, m, &entries, convert.V1Tables, func(mv int) bool { return mv <= 108 }, false)
		m.ScopeLevel = "" // presets did not exist for most of v1's life
	case v == 2:
		historicalLayout(t, m, &entries, v2Tables, func(mv int) bool { return mv < 20260803000000 }, true)
		m.ScopeLevel = backup.ScopeLevelStandard
	default:
		t.Fatalf("no fixture builder for v%d: a version the current writer no longer produces needs a historical builder here", v)
	}

	m.FormatVersion = v
	m.CrewshipVersionAtBackup = fmt.Sprintf("compat-fixture-v%d", v)
	m.SourceInstance = backup.Instance{Hostname: "compat-fixture", Platform: "linux/amd64"}
	m.CreatedAt = map[int]time.Time{
		1: time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC),
		2: time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC),
	}[v]
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	}

	var out bytes.Buffer
	if err := backup.WriteBundle(&out, m, bytes.NewReader(tarOf(t, entries)), backup.WriteBundleOptions{
		Recipients: []age.Recipient{id.Recipient()},
	}); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// currentLayout is what the current writer produces, plus the crew/
// section a real container would contribute (unit tests have no docker).
func currentLayout(m *backup.Manifest, entries *[]payloadEntry) {
	*entries = append(*entries,
		fileEntry("crew/"+fxCrewSlug+"/shared/.memory/CREW.md", fxCrewMemory),
		fileEntry("crew/"+fxCrewSlug+"/agents/"+fxAgentSlug+"/.memory/AGENT.md", fxAgentMemory),
	)
	for i := range m.Contents.Crews {
		if m.Contents.Crews[i].Slug == fxCrewSlug {
			m.Contents.Crews[i].MemoryIncluded = true
			m.Contents.Crews[i].CrewFilesIncluded = true
		}
	}
}

// historicalLayout reshapes a current payload into a pre-v3 one.
func historicalLayout(t *testing.T, m *backup.Manifest, entries *[]payloadEntry, tables []string, migrationExisted func(int) bool, memoryBlobs bool) {
	t.Helper()
	keep := map[string]bool{}
	for _, tb := range tables {
		keep[tb] = true
	}
	var out []payloadEntry
	for _, e := range *entries {
		name := e.hdr.Name
		switch {
		case name == "db/dump.json":
			e.body = filterDump(t, e.body, keep)
			e.hdr.Size = int64(len(e.body))
		case strings.HasPrefix(name, "memory-blobs/") && !memoryBlobs:
			continue
		case strings.HasPrefix(name, "crew/"), strings.HasPrefix(name, "page-projects/"):
			continue
		}
		out = append(out, e)
	}
	// Up to v2 the "memory" section held /output.
	out = append(out, fileEntry("memory/"+fxCrewSlug+"/notes.md", fxOutputContent))
	*entries = out

	var mig []int
	for _, mv := range m.SchemaMigrationVersions {
		if migrationExisted(mv) {
			mig = append(mig, mv)
		}
	}
	m.SchemaMigrationVersions = mig
	m.Contents.TableRowCounts = nil // #2009 came with v3
	m.Contents.MissingContainerCrews = nil
	if !memoryBlobs {
		m.Contents.MemoryBlobsIncluded = 0
		m.Contents.MemoryBlobsMissing = 0
	}
	for i := range m.Contents.Crews {
		c := &m.Contents.Crews[i]
		*c = backup.CrewSummary{
			ID: c.ID, Slug: c.Slug, Name: c.Name, AgentCount: c.AgentCount,
			// Pre-v3 writers set this for every crew without looking.
			MemoryIncluded: true,
		}
	}
}

func filterDump(t *testing.T, data []byte, keep map[string]bool) []byte {
	t.Helper()
	var d backup.DBDump
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&d); err != nil {
		t.Fatal(err)
	}
	for tb := range d.Tables {
		if !keep[tb] {
			delete(d.Tables, tb)
		}
	}
	out, err := json.Marshal(&d)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func fileEntry(name, body string) payloadEntry {
	return payloadEntry{
		hdr: tar.Header{
			Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body)),
			ModTime: time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC), Uid: 1001, Gid: 1002,
		},
		body: []byte(body),
	}
}

func tarOf(t *testing.T, entries []payloadEntry) []byte {
	t.Helper()
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].hdr.Name == "db/dump.json" && entries[j].hdr.Name != "db/dump.json"
	})
	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(zw)
	for _, e := range entries {
		h := e.hdr
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(e.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func openPayload(t *testing.T, path string, id age.Identity) (*backup.Manifest, []payloadEntry) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m, sealed, err := backup.ReadBundle(f)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := backup.DecryptStream(sealed, id)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := backup.NewTarZstReader(plain)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	var entries []payloadEntry
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, payloadEntry{hdr: *hdr, body: body})
	}
	return m, entries
}

// seedCompat writes the tenant every fixture is cut from: a workspace, a
// crew, an agent, an issue with a comment, a journal entry, a credential
// sealed with the TEST-ONLY vault key, and a memory version with its blob.
func seedCompat(t *testing.T, db *sql.DB, blobRoot string) {
	t.Helper()
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, q, args...); err != nil {
			t.Fatalf("seed: %v\n%s", err, q)
		}
	}
	exec(`INSERT INTO users (id, email, full_name) VALUES (?, 'admin@compat.test', 'Compat Admin')`, fxUser)
	exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Compat Workspace', ?)`, fxWorkspace, fxWorkspaceSlug)
	exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES (?, ?, 'Compat Crew', ?)`, fxCrew, fxWorkspace, fxCrewSlug)
	exec(`INSERT INTO agents (id, crew_id, workspace_id, name, slug, status) VALUES (?, ?, ?, 'Ada', ?, 'IDLE')`, fxAgent, fxCrew, fxWorkspace, fxAgentSlug)
	exec(`INSERT INTO crew_members (id, crew_id, user_id) VALUES ('cm_compat', ?, ?)`, fxCrew, fxUser)
	exec(`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at)
	      VALUES (?, ?, ?, ?, 'tr_compat', 'Compat issue', 'IN_PROGRESS', '2026-05-20 12:00:00')`, fxIssue, fxWorkspace, fxCrew, fxAgent)
	exec(`INSERT INTO mission_comments (id, mission_id, author_type, author_id, body) VALUES (?, ?, 'user', ?, ?)`, fxComment, fxIssue, fxUser, fxCommentBody)
	exec(`INSERT INTO journal_entries (id, workspace_id, crew_id, agent_id, entry_type, actor_type, summary)
	      VALUES (?, ?, ?, ?, 'user.note', 'user', 'compat fixture journal entry')`, fxJournal, fxWorkspace, fxCrew, fxAgent)
	sealed, err := encryption.Encrypt(fxSecret)
	if err != nil {
		t.Fatalf("seal credential: %v", err)
	}
	exec(`INSERT INTO credentials (id, workspace_id, name, type, status, encrypted_value, created_by)
	      VALUES (?, ?, 'COMPAT_TOKEN', 'SECRET', 'ACTIVE', ?, ?)`, fxCredential, fxWorkspace, sealed, fxUser)
	if _, err := memory.RecordVersion(ctx, db, memory.VersionRecord{
		WorkspaceID: fxWorkspace,
		Path:        fxMemoryPath,
		Tier:        memory.TierPins,
		Content:     []byte(fxMemoryContent),
		WrittenBy:   fxUser,
		BlobRoot:    blobRoot,
	}); err != nil {
		t.Fatalf("seed memory version: %v", err)
	}
}

// ---------------------------------------------------------------------
// helpers

func openMigratedDB(t *testing.T) *sql.DB {
	t.Helper()
	db := testutil.MigratedSQLDB(t)
	if err := database.SeedBundledSkills(context.Background(), db, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("seed bundled skills: %v", err)
	}
	return db
}

func loadIdentity(t *testing.T) *age.X25519Identity {
	t.Helper()
	ids, err := convert.ParseIdentityFile(filepath.Join(compatDir, identityFile))
	if err != nil {
		t.Fatalf("%v (generate with -update-compat-fixtures)", err)
	}
	id, ok := ids[0].(*age.X25519Identity)
	if !ok {
		t.Fatalf("fixture identity is %T", ids[0])
	}
	return id
}

func loadVaultKey(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(compatDir, vaultKeyFile))
	if err != nil {
		t.Fatalf("%v (generate with -update-compat-fixtures)", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			return line
		}
	}
	t.Fatal("vault key file has no key line")
	return ""
}

func readManifest(t *testing.T, path string) *backup.Manifest {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tr, err := backup.NewTarZstReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	for {
		hdr, err := tr.Next()
		if err != nil {
			t.Fatalf("no MANIFEST.json in %s: %v", path, err)
		}
		if hdr.Name == "MANIFEST.json" {
			b, err := io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}
			m, err := backup.ReadManifest(b)
			if err != nil {
				t.Fatal(err)
			}
			return m
		}
	}
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func fileDigest(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// v2Tables is internal/backup/dbdump.go BackupTables at the v2→v3
// boundary (a1ab934fb^), the last table list a v2 writer used.
var v2Tables = []string{
	"users", "workspaces", "skills", "crews", "chats", "workspace_files", "chat_attachments",
	"journal_entries", "journal_entry_priorities", "journal_chain_checkpoints", "workspace_members",
	"workspace_invitations", "workspace_mcp_servers", "backup_destinations", "scheduled_jobs",
	"port_exposures", "eval_runs", "gate_reward_history", "memory_health_snapshots",
	"feature_flag_overrides", "budget_limits", "cost_ledger", "hooks_config", "issue_counters",
	"subscriptions", "inbox_items", "notification_channels", "user_notification_prefs",
	"notification_channel_agents", "notification_templates", "webhooks", "routines", "schedules",
	"recurring_issues", "triage_rules", "workflow_templates", "saved_views", "hooks", "labels",
	"milestones", "projects", "missions", "crew_templates", "credentials", "crew_members", "agents",
	"crew_connections", "crew_mcp_servers", "credential_crews", "credential_bindings",
	"credential_fields", "agent_skills", "agent_mcp_bindings", "agent_credentials",
	"agent_config_history", "agent_runs", "checkpoints", "assignments", "approvals_queue",
	"pipelines", "pipeline_versions", "pipeline_schedules", "pipeline_webhooks", "pipeline_runs",
	"pipeline_run_step_outputs", "pipeline_routine_state", "pending_runs", "pipeline_tags",
	"pipeline_waitpoints", "gdpr_actions", "credential_audit", "credential_rotations",
	"chat_branches", "message_reactions", "message_feedback", "memory_relations",
	"memory_proposals", "memory_versions", "mission_tasks", "mission_activity", "mission_comments",
	"mission_labels", "mission_proposals", "mission_relations", "skill_invocations",
	"workflow_states", "captain_chats", "peer_cards", "user_peer_consent", "chat_participants",
	"chat_read_cursors", "run_tags", "routine_step_overrides", "composio_settings",
	"keeper_governance_settings", "user_models",
}
