package dockerfilesources

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// No image may carry the workbook test barrier (build tag
// crewship_testbarrier). The backend stage checks the binaries it just built,
// so a tag smuggled in through build args or GOFLAGS fails the image build.
func TestBackendStageRejectsTestBarrierBinaries(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	build := strings.Index(text, "-o /crewship ./cmd/crewship")
	guard := strings.Index(text, "RUN sh /usr/local/bin/check-no-test-barrier /crewship /crewship-sidecar")
	copyGuard := strings.Index(text, "COPY scripts/check-no-test-barrier.sh /usr/local/bin/check-no-test-barrier")
	runner := strings.Index(text, "\nFROM alpine:3.24 AS dockercli")
	if build < 0 || guard < 0 || copyGuard < 0 || runner < 0 || !(build < copyGuard && copyGuard < guard && guard < runner) {
		t.Fatalf("backend stage must run the test-barrier guard on its built binaries (build=%d copy=%d guard=%d runner=%d)", build, copyGuard, guard, runner)
	}
}
