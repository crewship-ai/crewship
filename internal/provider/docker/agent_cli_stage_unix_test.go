//go:build !windows

package docker

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"

	"github.com/crewship-ai/crewship/internal/provider"

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

func TestMappedAgentCLIAdmitsRetainedImmutableGeneration(t *testing.T) {
	root := mappingTempDir(t)
	previous := stageTestAgentCLI(t, root)
	binary, err := os.ReadFile(previous.Path)
	if err != nil {
		t.Fatal(err)
	}
	nextPath := filepath.Join(t.TempDir(), "crewship-agent-cli")
	if err := os.WriteFile(nextPath, append(binary, 1), 0755); err != nil {
		t.Fatal(err)
	}
	current, err := runtimestage.StageAgentCLI(root, nextPath)
	if err != nil {
		t.Fatal(err)
	}
	p := &Provider{pathMap: &daemonPathMap{local: root, daemon: "/daemon/storage"}}
	p.cfg.agentCLIArtifact = current
	source, err := p.daemonPathForLocal(previous.Path)
	if err != nil {
		t.Fatal(err)
	}
	actual := []container.MountPoint{{Type: mount.TypeBind, Source: source, Destination: agentCLIExecutable}}
	team := provider.CrewConfig{ID: "crew"}
	if err := p.checkMappedRuntimeMounts(t.Context(), actual, team); err != nil {
		t.Fatalf("read-only retained generation should reach idle reconciliation: %v", err)
	}
	actual[0].RW = true
	if err := p.checkMappedRuntimeMounts(t.Context(), actual, team); err == nil {
		t.Fatal("writable retained generation passed")
	}
	actual[0].RW = false
	actual[0].Source = filepath.Join("/daemon/storage/foreign", filepath.Base(source))
	if err := p.checkMappedRuntimeMounts(t.Context(), actual, team); err == nil {
		t.Fatal("unrelated directory with the matching basename passed")
	}
}
