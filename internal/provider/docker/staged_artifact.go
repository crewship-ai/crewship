package docker

import (
	"bytes"
	"context"
	"debug/elf"
	"fmt"
	"os"
	"path/filepath"

	"github.com/crewship-ai/crewship/internal/managedlaunch"
	"github.com/crewship-ai/crewship/internal/provider/runtimestage"
)

// stageArtifact reuses A1's immutable source publication and static ELF parser.
// Qualification reads the published bytes, never Docker's archive mount view.
func (p *Provider) stageArtifact(ctx context.Context, image string) (string, string, error) {
	source, e := p.stageTrustedFile(p.cfg.SidecarBinaryPath, managedlaunch.MaxArtifactBytes)
	if e != nil {
		return "", "", e
	}
	raw, e := os.ReadFile(source)
	if e != nil {
		return "", "", e
	}
	artifact, e := managedlaunch.Capture("/usr/local/bin/crewship-staged-artifact", raw)
	if e != nil {
		return "", "", fmt.Errorf("%w: %v", errStagedDenied, e)
	}
	f, e := elf.NewFile(bytes.NewReader(raw))
	if e != nil {
		return "", "", e
	}
	defer f.Close()
	img, e := p.client.ImageInspect(ctx, image)
	if e != nil {
		return "", "", e
	}
	machine := map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64}[img.Architecture]
	if img.Config == nil || len(img.Config.Volumes) > 0 || machine == 0 || f.Machine != machine {
		return "", "", errStagedDenied
	}
	return source, artifact.SHA256, nil
}

// The same A1 immutable publisher also pins the host-owned bootstrap script.
// It is executed only after keeper admission, unlike the static helper binary.
func (p *Provider) stageTrustedFile(source string, limit int64) (string, error) {
	info, e := os.Lstat(source)
	if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() > limit || !stagedOwned(info) {
		return "", fmt.Errorf("%w: trusted host artifact missing or unsafe owner/mode/type", errStagedDenied)
	}
	if e = os.MkdirAll(filepath.Join(p.cfg.OutputBasePath, runtimestage.DirName), 0755); e != nil {
		return "", e
	}
	return stageManagedLauncher(source, p.cfg.OutputBasePath)
}
