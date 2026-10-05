package convert

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/klauspost/compress/zstd"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/crewship-ai/crewship/internal/backup"
)

type refusingRecipient struct{}

func (refusingRecipient) Wrap([]byte) ([]*age.Stanza, error) {
	return nil, errors.New("recipient refused sealing")
}

func TestConvertSealingFailureJoinsRewriterAndRemovesTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.tar.zst")
	output := filepath.Join(dir, "converted.tar.zst")
	writeBundle(t, source, manifestAt(backup.FormatVersion), payloadOf(t, entry{"workspace/eng/data.txt", "original data"}), backup.WriteBundleOptions{NoEncrypt: true})
	original := fileSHA(t, source)
	registry, err := NewRegistry(Step{From: backup.FormatVersion, Manifest: func(*backup.Manifest, *PayloadIndex, *StepReport) error { return nil }, Entry: func(name string) (string, bool) { return name, true }})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := Convert(context.Background(), Options{BundlePath: source, OutPath: output, Registry: registry, Target: backup.FormatVersion + 1, Recipients: []age.Recipient{refusingRecipient{}}})
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "recipient refused sealing") {
			t.Fatalf("sealing failure lost: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("conversion did not join its rewriter after encryption refused the payload")
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed conversion published output: %v", err)
	}
	if got := fileSHA(t, source); got != original {
		t.Fatal("failed conversion modified its source")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "source.tar.zst" {
		t.Fatalf("failed conversion retained temporary output: %#v", entries)
	}
}

func TestConvertCollisionAfterIndexingPreservesExistingDestination(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.tar.zst")
	output := filepath.Join(dir, "output.tar.zst")
	writeBundle(t, source, manifestAt(backup.FormatVersion), payloadOf(t, entry{"workspace/eng/a", "data"}), backup.WriteBundleOptions{NoEncrypt: true})
	original := fileSHA(t, source)
	registry, err := NewRegistry(Step{From: backup.FormatVersion, Manifest: func(*backup.Manifest, *PayloadIndex, *StepReport) error {
		return os.WriteFile(output, []byte("concurrent output"), 0600)
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = Convert(t.Context(), Options{BundlePath: source, OutPath: output, Registry: registry, Target: backup.FormatVersion + 1, Recipients: []age.Recipient{newIdentity(t).Recipient()}})
	if !errors.Is(err, ErrOutputExists) {
		t.Fatalf("collision not refused: %v", err)
	}
	data, err := os.ReadFile(output)
	if err != nil || string(data) != "concurrent output" {
		t.Fatalf("concurrent output overwritten: %q %v", data, err)
	}
	if fileSHA(t, source) != original {
		t.Fatal("source changed")
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, ".crewship-convert-*"))
	if len(leftovers) != 0 {
		t.Fatalf("temporary files retained: %v", leftovers)
	}
}
func TestConvertRequiredPathsAndUnavailableOutput(t *testing.T) {
	if _, err := Convert(t.Context(), Options{}); err == nil || !strings.Contains(err.Error(), "bundle path required") {
		t.Fatalf("missing source: %v", err)
	}
	dir := t.TempDir()
	source := filepath.Join(dir, "source.tar.zst")
	writeBundle(t, source, manifestAt(1), v1Payload(t), backup.WriteBundleOptions{NoEncrypt: true})
	id := newIdentity(t)
	if _, err := Convert(t.Context(), Options{BundlePath: source}); err == nil || !strings.Contains(err.Error(), "output path required") {
		t.Fatalf("missing output: %v", err)
	}
	if _, err := Convert(t.Context(), Options{BundlePath: source, OutPath: filepath.Join(dir, "missing", "out"), Recipients: []age.Recipient{id.Recipient()}}); err == nil || !strings.Contains(err.Error(), "temp file") {
		t.Fatalf("missing output parent: %v", err)
	}
	empty, _ := NewRegistry()
	if _, err := Convert(t.Context(), Options{BundlePath: source, OutPath: filepath.Join(dir, "out"), Registry: empty}); !errors.Is(err, ErrNoConverter) {
		t.Fatalf("missing conversion step: %v", err)
	}
	link := filepath.Join(dir, "same")
	if err := os.Link(source, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Convert(t.Context(), Options{BundlePath: source, OutPath: link}); err == nil || !strings.Contains(err.Error(), "never modified") {
		t.Fatalf("hardlink alias accepted: %v", err)
	}
}
func TestBundleStructureFailuresAreNotPartialManifests(t *testing.T) {
	m := manifestAt(1)
	m.Checksums.PayloadSHA256 = strings.Repeat("0", 64)
	manifest, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		entries []entry
		want    string
	}{{"empty", nil, "MANIFEST.json missing"}, {"no payload", []entry{{manifestMember, string(manifest)}}, "payload missing"}, {"payload first", []entry{{payloadMember, "data"}, {manifestMember, string(manifest)}}, "payload before"}, {"invalid manifest", []entry{{manifestMember, "not-json"}}, "manifest"}} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "broken.tar.zst")
			if err := os.WriteFile(path, payloadOf(t, tc.entries...), 0600); err != nil {
				t.Fatal(err)
			}
			src, err := openBundle(path)
			if err == nil || src != nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tc.want)) {
				t.Fatalf("broken archive accepted: %#v %v", src, err)
			}
		})
	}
	path := filepath.Join(t.TempDir(), "broken.tar.zst")
	if err := os.WriteFile(path, []byte("not zstd"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := openBundle(path); err == nil {
		t.Fatal("invalid compressed stream accepted")
	}
	if _, err := openBundle(path + ".missing"); err == nil {
		t.Fatal("missing archive accepted")
	}
}
func TestIndexRejectsMalformedDumpAndTruncatedPayload(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload []byte
	}{{"dump", payloadOf(t, entry{dumpEntry, "not-json"})}, {"compression", []byte("not-zstd")}} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "broken.tar.zst")
			writeBundle(t, path, manifestAt(1), tc.payload, backup.WriteBundleOptions{NoEncrypt: true})
			src, err := openBundle(path)
			if err != nil {
				t.Fatal(err)
			}
			defer src.Close()
			if idx, err := indexPayload(src, Options{}); err == nil || idx != nil {
				t.Fatalf("broken payload indexed: %#v %v", idx, err)
			}
		})
	}
}
func TestRewriteRejectsCancellationMalformedDumpAndStepFailure(t *testing.T) {
	payload := payloadOf(t, entry{dumpEntry, dumpJSON(t, map[string][]map[string]any{})})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := rewritePayload(ctx, bytes.NewReader(payload), io.Discard, nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled rewrite: %v", err)
	}
	if err := rewritePayload(t.Context(), bytes.NewReader([]byte("broken")), io.Discard, nil, nil); err == nil {
		t.Fatal("invalid payload rewritten")
	}
	malformed := payloadOf(t, entry{dumpEntry, "not-json"})
	if err := rewritePayload(t.Context(), bytes.NewReader(malformed), io.Discard, nil, nil); err == nil || !strings.Contains(err.Error(), "parse db/dump.json") {
		t.Fatalf("invalid dump rewritten: %v", err)
	}
	denied := errors.New("migration refused")
	chain := []Step{{From: 4, Dump: func(*backup.DBDump, *StepReport) error { return denied }}}
	if err := rewritePayload(t.Context(), bytes.NewReader(payload), io.Discard, chain, make([]StepReport, 1)); !errors.Is(err, denied) {
		t.Fatalf("step failure lost: %v", err)
	}
}
func TestRewritePreservesDirectoriesAndRefusesTruncatedBodies(t *testing.T) {
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	if err := tw.WriteHeader(&tar.Header{Name: "workspace/eng/", Typeflag: tar.TypeDir, Mode: 0750, Uid: 1001, Gid: 1001}); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	enc, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer enc.Close()
	var output bytes.Buffer
	if err := rewritePayload(t.Context(), bytes.NewReader(enc.EncodeAll(raw.Bytes(), nil)), &output, nil, nil); err != nil {
		t.Fatal(err)
	}
	tr, err := backup.NewTarZstReader(&output)
	if err != nil {
		t.Fatal(err)
	}
	defer tr.Close()
	hdr, err := tr.Next()
	if err != nil || hdr.Typeflag != tar.TypeDir || hdr.Uid != 1001 || hdr.Gid != 1001 {
		t.Fatalf("directory metadata changed: %#v %v", hdr, err)
	}
	for _, name := range []string{dumpEntry, "workspace/eng/file"} {
		raw.Reset()
		tw = tar.NewWriter(&raw)
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0600, Size: 100}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte("short")); err != nil {
			t.Fatal(err)
		}
		if err := rewritePayload(t.Context(), bytes.NewReader(enc.EncodeAll(raw.Bytes(), nil)), io.Discard, nil, nil); err == nil {
			t.Fatalf("truncated %s accepted", name)
		}
	}
}
func TestIdentityAndRecipientInputsFailClearly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "identity")
	id := newIdentity(t)
	if _, err := ParseIdentityFile(path); err == nil {
		t.Fatal("missing identity accepted")
	}
	if err := os.WriteFile(path, []byte("invalid identity"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseIdentityFile(path); err == nil {
		t.Fatal("malformed identity accepted")
	}
	if err := os.WriteFile(path, []byte("# comment\n"+id.String()+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ids, err := ParseIdentityFile(path)
	if err != nil || len(ids) != 1 {
		t.Fatalf("identity parse: %v %v", ids, err)
	}
	rs, err := ParseRecipients([]string{"  " + id.Recipient().String() + " "})
	if err != nil || len(rs) != 1 || recipientString(rs[0]) != id.Recipient().String() {
		t.Fatalf("recipient parse: %v %v", rs, err)
	}
	if _, err := ParseRecipients([]string{"invalid"}); err == nil {
		t.Fatal("malformed recipient accepted")
	}
}

func TestLegacyConversionReportsObservedFilesAndIrrecoverableData(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "legacy.tar.zst")
	output := filepath.Join(dir, "converted.tar.zst")
	m := manifestAt(1)
	m.Contents.MissingContainerCrews = []string{"offline"}
	id := newIdentity(t)
	payload := payloadOf(t, entry{dumpEntry, dumpJSON(t, map[string][]map[string]any{"crew_integrations": {{"id": "legacy"}}, "missions": {{"id": "later"}}, "memory_versions": {{"id": "missing-content"}}})}, entry{"crew/eng/.memory/note.md", "recorded memory"}, entry{"crew/eng/config.json", "{}"}, entry{"memory/eng/output.txt", "recorded output"})
	writeBundle(t, source, m, payload, backup.WriteBundleOptions{NoEncrypt: true})
	report, err := Convert(t.Context(), Options{BundlePath: source, OutPath: output, Recipients: []age.Recipient{id.Recipient()}, ConverterVersion: "test", Now: func() time.Time { return fixedTime }})
	if err != nil {
		t.Fatal(err)
	}
	text := Describe(report)
	for _, want := range []string{"The original was not modified", "changed:", "cannot be recovered:", "Warnings:", "crew_integrations", "outside the v1 table list", "no memory-blobs", "offline", "crew/eng/"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing operator finding %q: %s", want, text)
		}
	}
	converted, plain := readConverted(t, output, id)
	if !bytes.Equal(plain, payload) {
		t.Fatal("manifest-only conversion changed payload")
	}
	crew := converted.Contents.Crews[0]
	if !crew.MemoryIncluded || !crew.OutputIncluded || !crew.CrewFilesIncluded {
		t.Fatalf("observed data not reflected in flags: %#v", crew)
	}
	if len(converted.Contents.TableRowCounts) != 0 {
		t.Fatal("conversion invented original row counts")
	}
	current, err := Convert(t.Context(), Options{BundlePath: output, Identities: []age.Identity{id}})
	if err != nil {
		t.Fatal(err)
	}
	if text := Describe(current); !strings.Contains(text, "nothing to convert, nothing written") {
		t.Fatalf("current bundle report: %s", text)
	}
}
func TestManifestOnlyLegacyPayloadWithoutDump(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "legacy.tar.zst")
	output := filepath.Join(dir, "converted.tar.zst")
	id := newIdentity(t)
	writeBundle(t, source, manifestAt(1), payloadOf(t, entry{"workspace/eng/note.txt", "note"}), backup.WriteBundleOptions{NoEncrypt: true})
	report, err := Convert(t.Context(), Options{BundlePath: source, OutPath: output, Recipients: []age.Recipient{id.Recipient()}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(report.Warnings, " "), "per-table row counts") {
		t.Fatal("reported a dump that did not exist")
	}
}
func TestOutputSealingRequiresRecoverableRecipientsOrExplicitOverride(t *testing.T) {
	for _, tc := range []struct {
		name   string
		m      backup.Encryption
		opts   Options
		denied bool
		phrase string
	}{
		{"no recorded recipients", backup.Encryption{Enabled: true}, Options{}, true, ""},
		{"unsupported recipient", backup.Encryption{Enabled: true, Recipients: []string{"unsupported-recipient"}}, Options{}, true, ""},
		{"passphrase marker", backup.Encryption{Enabled: true, Recipients: []string{"scrypt"}}, Options{Passphrase: "fixture-passphrase"}, false, "fixture-passphrase"},
		{"explicit passphrase", backup.Encryption{}, Options{OutPassphrase: "new-passphrase"}, false, "new-passphrase"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			seal, description, err := outputSealing(&backup.Manifest{Encryption: tc.m}, tc.opts)
			if tc.denied {
				if !errors.Is(err, ErrNoOutputKey) {
					t.Fatalf("unusable output key accepted: %v", err)
				}
				return
			}
			if err != nil || seal.Passphrase != tc.phrase || !strings.Contains(description, "passphrase") {
				t.Fatalf("sealing override: %#v %s %v", seal, description, err)
			}
		})
	}
}
