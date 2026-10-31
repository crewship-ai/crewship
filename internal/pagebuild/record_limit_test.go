package pagebuild

import (
	"fmt"
	"strings"
	"testing"
)

// B11 (G4): the writer must not produce a record the reader refuses.
func TestWriteBuildImageRecordRespectsReaderLimit(t *testing.T) {
	dir := t.TempDir()
	rec := &BuildImageRecord{Images: map[string]RecordedImage{}}
	id := "sha256:" + strings.Repeat("a", 64)
	for i := 0; i < 600; i++ {
		fp := fmt.Sprintf("%064d", i)
		rec.Images[fp] = RecordedImage{ID: id, Tag: ImageTag(fp)}
	}
	if err := WriteBuildImageRecord(dir, rec); err == nil {
		// If it was written, the reader must accept it.
		if _, rerr := ReadBuildImageRecord(dir); rerr != nil {
			t.Fatalf("writer produced a record the reader rejects: %v", rerr)
		}
		t.Fatal("oversized record was written")
	}
	// A refused write leaves nothing behind that the reader would choke on.
	if _, err := ReadBuildImageRecord(dir); err == nil {
		t.Fatal("record exists after refused write")
	}
}
