//go:build !windows

package docker

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/provider/runtimestage"
)

// stageTestAgentCLI stages a structural (never executed) static ELF through
// the real staging path, so attestation checks verify genuine staged bytes.
func stageTestAgentCLI(t *testing.T, root string) *runtimestage.AgentCLIArtifact {
	t.Helper()
	raw := make([]byte, 120)
	copy(raw, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1})
	binary.LittleEndian.PutUint16(raw[16:], 2)
	binary.LittleEndian.PutUint16(raw[18:], 62)
	binary.LittleEndian.PutUint32(raw[20:], 1)
	binary.LittleEndian.PutUint64(raw[32:], 64)
	binary.LittleEndian.PutUint16(raw[52:], 64)
	binary.LittleEndian.PutUint16(raw[54:], 56)
	binary.LittleEndian.PutUint16(raw[56:], 1)
	binary.LittleEndian.PutUint32(raw[64:], 1)
	source := filepath.Join(t.TempDir(), "crewship-agent-cli")
	if err := os.WriteFile(source, raw, 0755); err != nil {
		t.Fatal(err)
	}
	a, err := runtimestage.StageAgentCLI(root, source)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
