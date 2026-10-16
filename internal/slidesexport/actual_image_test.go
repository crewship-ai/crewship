//go:build slides_export_runtime

package slidesexport

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

// Opt-in build tag makes ordinary Go tests image-independent without introducing
// a skipped acceptance site. This test requires the actual authorized image job.
func TestActualExportImageCorpus(t *testing.T) {
	if os.Getenv("CREWSHIP_JOB_ID") != "" {
		t.Fatal("full image profile requires the authorized isolated CI runner; do not bypass the local gateway")
	}
	image := os.Getenv("CREWSHIP_TEST_SLIDES_EXPORT_IMAGE")
	if !imagePattern.MatchString(image) {
		t.Fatal("actual immutable image required")
	}
	raw, err := os.ReadFile("../../tools/slides-export/testdata/supported.json")
	if err != nil {
		t.Fatal(err)
	}
	input, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{Image: image, OwnerID: "actual_ci", JournalDir: t.TempDir()}
	defer r.Close()
	lease, err := r.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	b := Binding{Mode: "report", PageID: input.Deck.DeckID, Revision: input.Revision, ContentHash: input.ContentHash}
	started := time.Now()
	report, err := lease.Run(t.Context(), raw, b)
	if err != nil {
		t.Fatal(err)
	}
	evidence := os.Getenv("CREWSHIP_TEST_SLIDES_EXPORT_EVIDENCE")
	if evidence == "" {
		t.Fatal("owned evidence directory required")
	}
	if err = os.MkdirAll(evidence, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(evidence, "report.json"), report.Report, 0600); err != nil {
		t.Fatal(err)
	}
	result := map[string]any{"image": image, "profile_sha256": report.ProfileSHA256, "report_hash": report.ReportHash, "page_id": b.PageID, "revision": b.Revision, "content_hash": b.ContentHash, "qualification": "actual image corpus only; cancellation/uncertain cleanup lifecycle remains separate"}
	for _, mode := range []string{"pptx", "pdf"} {
		b.Mode = mode
		b.ReportHash = report.ReportHash
		frame, err := lease.Run(t.Context(), raw, b)
		if err != nil {
			t.Fatal(err)
		}
		if len(frame.Binary) == 0 {
			t.Fatal("empty actual output")
		}
		if mode == "pptx" {
			verifyActualPPTX(t, frame.Binary, input)
		} else {
			if bytes.Count(frame.Binary, []byte("/Type /Page\n")) != 5 {
				t.Fatal("actual PDF page structure")
			}
		}
		result[mode+"_bytes"] = len(frame.Binary)
		result[mode+"_sha256"] = frame.SHA256
		if err = os.WriteFile(filepath.Join(evidence, "deck."+mode), frame.Binary, 0600); err != nil {
			t.Fatal(err)
		}
	}
	result["elapsed_ms"] = time.Since(started).Milliseconds()
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(evidence, "image-corpus.json"), append(encoded, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
}
func verifyActualPPTX(t *testing.T, data []byte, input Input) {
	t.Helper()
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	slides := 0
	image := false
	table := false
	lf := false
	for _, entry := range archive.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		reader, err := entry.Open()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(io.LimitReader(reader, MaxInputBytes+1))
		_ = reader.Close()
		if err != nil || len(raw) > MaxInputBytes {
			t.Fatal("unbounded ZIP entry")
		}
		if regexp.MustCompile(`^ppt/slides/slide[0-9]+\.xml$`).MatchString(entry.Name) {
			slides++
			if bytes.Contains(raw, []byte("native_table")) && bytes.Contains(raw, []byte("<a:tbl>")) {
				table = true
			}
			if bytes.Contains(raw, []byte("czech_title")) && bytes.Contains(raw, []byte("Druhý řádek českého textu")) {
				lf = true
			}
		}
		if hash(raw) == input.Assets[0].ExpectedSHA256 {
			image = true
		}
		if bytes.Contains(raw, []byte(`TargetMode="External"`)) {
			t.Fatal("external relationship in actual image output")
		}
	}
	if slides != 5 || !image || !table || !lf {
		t.Fatalf("actual native package slides=%d raster=%t table=%t LFtext=%t", slides, image, table, lf)
	}
}
