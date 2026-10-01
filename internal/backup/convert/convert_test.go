package convert

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
)

var fixedTime = time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)

type entry struct {
	name string
	body string
}

// payloadOf builds an inner payload tar.zst the way the collector does.
func payloadOf(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	w, err := backup.NewTarZstWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if err := w.WriteFile(e.name, 0o644, fixedTime, []byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func dumpJSON(t *testing.T, tables map[string][]map[string]any) string {
	t.Helper()
	b, err := json.Marshal(backup.DBDump{WorkspaceID: "ws_1", Tables: tables})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func manifestAt(version int) *backup.Manifest {
	return &backup.Manifest{
		FormatVersion:           version,
		CrewshipVersionAtBackup: "test",
		SchemaMigrationVersions: []int{1, 2, 3},
		Scope:                   backup.ScopeWorkspace,
		CompatibleTargets:       []backup.Target{backup.TargetAnyInstance},
		CreatedAt:               fixedTime,
		CreatedBy:               backup.Actor{UserID: "u_admin", Email: "admin@test", Role: "ADMIN"},
		SourceInstance:          backup.Instance{Hostname: "fixture", Platform: "linux/amd64"},
		Contents: backup.Contents{
			Workspace: &backup.WorkspaceSummary{ID: "ws_1", Slug: "acme", Name: "Acme"},
			Crews:     []backup.CrewSummary{{ID: "c_eng", Slug: "eng", Name: "Eng", MemoryIncluded: true, AgentCount: 1}},
		},
	}
}

func writeBundle(t *testing.T, path string, m *backup.Manifest, payload []byte, opts backup.WriteBundleOptions) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := backup.WriteBundle(f, m, bytes.NewReader(payload), opts); err != nil {
		t.Fatal(err)
	}
}

func fileSHA(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// readConverted opens a converted bundle and returns its manifest and
// decrypted inner payload bytes.
func readConverted(t *testing.T, path string, ids ...age.Identity) (*backup.Manifest, []byte) {
	t.Helper()
	src, err := openBundle(path)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	plain, err := decryptPayload(src.payload, src.manifest, Options{Identities: ids})
	if err != nil {
		t.Fatal(err)
	}
	b, err := io.ReadAll(plain)
	if err != nil {
		t.Fatal(err)
	}
	return src.manifest, b
}

func newIdentity(t *testing.T) *age.X25519Identity {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func v1Payload(t *testing.T) []byte {
	return payloadOf(t,
		entry{"db/dump.json", dumpJSON(t, map[string][]map[string]any{
			"workspaces":      {{"id": "ws_1", "slug": "acme", "name": "Acme"}},
			"crews":           {{"id": "c_eng", "workspace_id": "ws_1", "slug": "eng", "name": "Eng"}},
			"agents":          {{"id": "a_1", "crew_id": "c_eng", "workspace_id": "ws_1", "slug": "alice"}},
			"journal_entries": {{"id": "j_1", "workspace_id": "ws_1", "summary": "hello"}},
		})},
		entry{"memory/eng/notes.md", "what /output held\n"},
	)
}

func TestConvert_V1ToCurrent(t *testing.T) {
	dir := t.TempDir()
	id := newIdentity(t)
	src := filepath.Join(dir, "v1.tar.zst")
	payload := v1Payload(t)
	writeBundle(t, src, manifestAt(1), payload, backup.WriteBundleOptions{Recipients: []age.Recipient{id.Recipient()}})
	before := fileSHA(t, src)

	out := filepath.Join(dir, "v1.converted.tar.zst")
	rep, err := Convert(context.Background(), Options{
		BundlePath: src, OutPath: out, Identities: []age.Identity{id},
		ConverterVersion: "test", Now: func() time.Time { return fixedTime },
	})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}

	if got := fileSHA(t, src); got != before {
		t.Fatal("the original bundle was modified")
	}
	if rep.From != 1 || rep.To != backup.FormatVersion || len(rep.Steps) != backup.FormatVersion-1 {
		t.Fatalf("report from/to/steps = %d/%d/%d", rep.From, rep.To, len(rep.Steps))
	}
	if rep.Output != out {
		t.Errorf("report output = %q", rep.Output)
	}
	all := strings.Join(rep.Unrecoverable, "\n")
	for _, want := range []string{"issues", "credentials", "memory versions", "/crew"} {
		if !strings.Contains(all, want) {
			t.Errorf("unrecoverable does not mention %q:\n%s", want, all)
		}
	}
	if !strings.Contains(strings.Join(rep.Warnings, "\n"), "#2009") {
		t.Errorf("missing row-count warning: %v", rep.Warnings)
	}

	m, plain := readConverted(t, out, id)
	if m.FormatVersion != backup.FormatVersion {
		t.Errorf("format_version = %d", m.FormatVersion)
	}
	if m.ScopeLevel != backup.ScopeLevelStandard {
		t.Errorf("scope_level = %q", m.ScopeLevel)
	}
	c := m.Contents.Crews[0]
	if c.MemoryIncluded || !c.OutputIncluded || c.CrewFilesIncluded {
		t.Errorf("crew flags memory=%t output=%t crew_files=%t; want false/true/false", c.MemoryIncluded, c.OutputIncluded, c.CrewFilesIncluded)
	}
	if !c.HasFilesystemSections(m.FormatVersion) {
		t.Error("the /output section must still get a docker phase after conversion")
	}
	if !bytes.Equal(plain, payload) {
		t.Error("the payload must pass through unchanged when no step rewrites it")
	}
	if len(m.Encryption.Recipients) != 1 || m.Encryption.Recipients[0] != id.Recipient().String() {
		t.Errorf("recipients = %v; want the source's", m.Encryption.Recipients)
	}
	if m.Conversion == nil || m.Conversion.FromFormatVersion != 1 || m.Conversion.SourcePayloadSHA256 != rep.SourcePayloadSHA256 {
		t.Errorf("conversion provenance = %+v", m.Conversion)
	}
	if len(m.SchemaMigrationVersions) != 3 {
		t.Errorf("schema_migration_versions must be kept, got %v", m.SchemaMigrationVersions)
	}

	// The current reader opens it directly and restores the old section.
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, _, err := backup.ReadBundle(f); err != nil {
		t.Fatalf("ReadBundle on converted bundle: %v", err)
	}
	ex, err := backup.ExtractPayload(context.Background(), bytes.NewReader(plain))
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()
	if _, ok, _ := ex.OpenMemory(context.Background(), "eng"); !ok {
		t.Error("the memory/ (/output) section did not survive conversion")
	}
}

func TestConvert_AlreadyCurrentWritesNothing(t *testing.T) {
	dir := t.TempDir()
	id := newIdentity(t)
	src := filepath.Join(dir, "cur.tar.zst")
	writeBundle(t, src, manifestAt(backup.FormatVersion), v1Payload(t), backup.WriteBundleOptions{Recipients: []age.Recipient{id.Recipient()}})
	out := filepath.Join(dir, "out.tar.zst")
	rep, err := Convert(context.Background(), Options{BundlePath: src, OutPath: out, Identities: []age.Identity{id}})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Steps) != 0 || rep.Output != "" {
		t.Errorf("report = %+v; want no steps, no output", rep)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Error("an already-current bundle must not produce an output file")
	}
	if !strings.Contains(Describe(rep), "nothing to convert") {
		t.Errorf("Describe: %s", Describe(rep))
	}
}

func TestConvert_Refusals(t *testing.T) {
	dir := t.TempDir()
	id := newIdentity(t)
	src := filepath.Join(dir, "v1.tar.zst")
	writeBundle(t, src, manifestAt(1), v1Payload(t), backup.WriteBundleOptions{Recipients: []age.Recipient{id.Recipient()}})
	existing := filepath.Join(dir, "exists.tar.zst")
	if err := os.WriteFile(existing, []byte("keep me"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		opts Options
		want error
		text string
	}{
		{"output exists", Options{BundlePath: src, OutPath: existing, Identities: []age.Identity{id}}, ErrOutputExists, ""},
		{"output is the bundle", Options{BundlePath: src, OutPath: src, Identities: []age.Identity{id}}, nil, "never modified"},
		{"no key", Options{BundlePath: src, OutPath: filepath.Join(dir, "a.tar.zst")}, nil, "--identity"},
		{"wrong key", Options{BundlePath: src, OutPath: filepath.Join(dir, "b.tar.zst"), Identities: []age.Identity{newIdentity(t)}}, backup.ErrDecryption, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Convert(context.Background(), tt.opts)
			if err == nil {
				t.Fatal("want an error")
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Errorf("err = %v; want %v", err, tt.want)
			}
			if tt.text != "" && !strings.Contains(err.Error(), tt.text) {
				t.Errorf("err = %v; want it to mention %q", err, tt.text)
			}
			if tt.opts.OutPath != src && tt.opts.OutPath != existing {
				if _, statErr := os.Stat(tt.opts.OutPath); !os.IsNotExist(statErr) {
					t.Error("a failed conversion left an output file")
				}
			}
		})
	}
	if b, _ := os.ReadFile(existing); string(b) != "keep me" {
		t.Error("an existing output file was clobbered")
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, ".crewship-convert-*"))
	if len(leftovers) > 0 {
		t.Errorf("temp files left behind: %v", leftovers)
	}
}

func TestConvert_RefusesTamperedPayload(t *testing.T) {
	dir := t.TempDir()
	id := newIdentity(t)
	var sealed bytes.Buffer
	if _, _, err := backup.SealPayload(&sealed, bytes.NewReader(v1Payload(t)), backup.WriteBundleOptions{Recipients: []age.Recipient{id.Recipient()}}); err != nil {
		t.Fatal(err)
	}
	m := manifestAt(1)
	m.Encryption = backup.Encryption{Enabled: true, Algorithm: backup.EncryptionAlgorithm, Recipients: []string{id.Recipient().String()}}
	m.Checksums.PayloadSHA256 = "sha256:" + strings.Repeat("0", 64)
	src := filepath.Join(dir, "tampered.tar.zst")
	f, err := os.Create(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := backup.WriteBundleStream(f, m, &sealed, int64(sealed.Len())); err != nil {
		t.Fatal(err)
	}
	f.Close()
	_, err = Convert(context.Background(), Options{BundlePath: src, OutPath: filepath.Join(dir, "out.tar.zst"), Identities: []age.Identity{id}})
	if !errors.Is(err, backup.ErrInvalidChecksum) {
		t.Fatalf("err = %v; want ErrInvalidChecksum", err)
	}
}

func TestConvert_OutputSealing(t *testing.T) {
	ctx := context.Background()
	id := newIdentity(t)
	other := newIdentity(t)

	t.Run("passphrase source reuses the passphrase", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "p.tar.zst")
		writeBundle(t, src, manifestAt(1), v1Payload(t), backup.WriteBundleOptions{Passphrase: "correct horse"})
		out := filepath.Join(dir, "o.tar.zst")
		if _, err := Convert(ctx, Options{BundlePath: src, OutPath: out, Passphrase: "correct horse"}); err != nil {
			t.Fatal(err)
		}
		s, err := openBundle(out)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if s.manifest.Encryption.KeyDerivation != "scrypt" {
			t.Errorf("encryption = %+v", s.manifest.Encryption)
		}
		if _, err := decryptPayload(s.payload, s.manifest, Options{Passphrase: "correct horse"}); err != nil {
			t.Errorf("the passphrase that opened the source must open the output: %v", err)
		}
	})

	t.Run("new recipient replaces the old one", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "r.tar.zst")
		writeBundle(t, src, manifestAt(2), v1Payload(t), backup.WriteBundleOptions{Recipients: []age.Recipient{id.Recipient()}})
		out := filepath.Join(dir, "o.tar.zst")
		rep, err := Convert(ctx, Options{BundlePath: src, OutPath: out, Identities: []age.Identity{id}, Recipients: []age.Recipient{other.Recipient()}})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(rep.Encryption, other.Recipient().String()) {
			t.Errorf("report encryption = %q", rep.Encryption)
		}
		s, err := openBundle(out)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		if _, err := decryptPayload(s.payload, s.manifest, Options{Identities: []age.Identity{id}}); !errors.Is(err, backup.ErrDecryption) {
			t.Errorf("the old identity still opens a bundle re-encrypted to a new recipient: %v", err)
		}
	})

	t.Run("unencrypted legacy source needs an output key", func(t *testing.T) {
		dir := t.TempDir()
		src := filepath.Join(dir, "plain.tar.zst")
		writeBundle(t, src, manifestAt(1), v1Payload(t), backup.WriteBundleOptions{NoEncrypt: true})
		if _, err := Convert(ctx, Options{BundlePath: src, OutPath: filepath.Join(dir, "a.tar.zst")}); !errors.Is(err, ErrNoOutputKey) {
			t.Fatalf("err = %v; want ErrNoOutputKey", err)
		}
		out := filepath.Join(dir, "b.tar.zst")
		if _, err := Convert(ctx, Options{BundlePath: src, OutPath: out, Recipients: []age.Recipient{other.Recipient()}}); err != nil {
			t.Fatal(err)
		}
		m, _ := readConverted(t, out, other)
		if !m.Encryption.Enabled {
			t.Error("a converted bundle must be encrypted")
		}
	})
}

// TestConvert_PayloadRewritingStep exercises the re-pack path a future
// step will need (renaming a table, dropping a section), with a
// synthetic step beyond the current version.
func TestConvert_PayloadRewritingStep(t *testing.T) {
	dir := t.TempDir()
	id := newIdentity(t)
	src := filepath.Join(dir, "cur.tar.zst")
	writeBundle(t, src, manifestAt(backup.FormatVersion), payloadOf(t,
		entry{"db/dump.json", dumpJSON(t, map[string][]map[string]any{
			"old_table": {{"id": "x1", "big": 9007199254740993}},
		})},
		entry{"junk/file", "drop me"},
		entry{"memory/eng/a.md", "keep me"},
	), backup.WriteBundleOptions{Recipients: []age.Recipient{id.Recipient()}})

	reg, err := NewRegistry(Step{
		From:     backup.FormatVersion,
		Summary:  "synthetic",
		Manifest: func(*backup.Manifest, *PayloadIndex, *StepReport) error { return nil },
		Dump: func(d *backup.DBDump, r *StepReport) error {
			d.Tables["new_table"] = d.Tables["old_table"]
			delete(d.Tables, "old_table")
			r.change("renamed old_table")
			r.warn("dump rewritten")
			return nil
		},
		Entry: func(name string) (string, bool) { return name, !strings.HasPrefix(name, "junk/") },
	})
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "next.tar.zst")
	rep, err := Convert(context.Background(), Options{
		BundlePath: src, OutPath: out, Identities: []age.Identity{id},
		Registry: reg, Target: backup.FormatVersion + 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(rep.Warnings, ","), "dump rewritten") {
		t.Errorf("a warning recorded during the dump rewrite was lost: %v", rep.Warnings)
	}
	_, plain := readConverted(t, out, id)
	tr, err := backup.NewTarZstReader(bytes.NewReader(plain))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]string{}
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
			names[hdr.Name] = string(b)
		}
	}
	if _, ok := names["junk/file"]; ok {
		t.Error("Entry drop was not applied")
	}
	if names["memory/eng/a.md"] != "keep me" {
		t.Error("an untouched entry lost its content")
	}
	if !strings.Contains(names["db/dump.json"], `"new_table"`) || strings.Contains(names["db/dump.json"], "old_table") {
		t.Errorf("dump not rewritten: %s", names["db/dump.json"])
	}
	if !strings.Contains(names["db/dump.json"], "9007199254740993") {
		t.Errorf("a large integer lost precision in the rewrite: %s", names["db/dump.json"])
	}
}

func TestRegistry(t *testing.T) {
	ok := func(*backup.Manifest, *PayloadIndex, *StepReport) error { return nil }
	if _, err := NewRegistry(Step{From: 1, Manifest: ok}, Step{From: 1, Manifest: ok}); err == nil {
		t.Error("duplicate steps accepted")
	}
	if _, err := NewRegistry(Step{From: 1}); err == nil {
		t.Error("a step without a Manifest func accepted")
	}
	if _, err := NewRegistry(Step{From: 0, Manifest: ok}); err == nil {
		t.Error("a step from v0 accepted")
	}
	reg, err := NewRegistry(Step{From: 1, Manifest: ok}, Step{From: 3, Manifest: ok})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Chain(1, 4); !errors.Is(err, ErrNoConverter) {
		t.Errorf("a gap in the chain must be ErrNoConverter, got %v", err)
	}
	if steps, err := reg.Chain(3, 4); err != nil || len(steps) != 1 {
		t.Errorf("Chain(3,4) = %d steps, %v", len(steps), err)
	}
}
