package devcontainer

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// writeToolchainInventory is shared by exec/commit and Dockerfile recording.
// Probes run as the agent; Crewship supplies no provider credentials. Their
// time and output are bounded. Unsupported --version records unknown evidence.
func writeToolchainInventory(ctx context.Context, containerID string, bins []string, containerEnv map[string]string, exec ExecFunc) error {
	if len(bins) == 0 {
		return nil
	}
	known := map[string]bool{}
	for _, cli := range adapterCLIs {
		known[cli.Binary] = true
	}
	var quoted []string
	for _, binary := range SortedBinaries(bins) {
		if !known[binary] {
			continue
		}
		quoted = append(quoted, shellQuote(binary))
	}
	if len(quoted) == 0 {
		return nil
	}
	setup := "rm -rf " + toolchainDirectory + " && mkdir -p " + toolchainDirectory + " && chown 1001:1001 " + toolchainDirectory + " && chmod 0700 " + toolchainDirectory
	if _, code, err := exec(ctx, containerID, []string{"sh", "-c", setup}, "0:0", nil); err != nil || code != 0 {
		return fmt.Errorf("prepare toolchain inventory")
	}
	searchPath := containerEnv["PATH"]
	if searchPath == "" {
		searchPath = strings.Join(AgentToolPathDirs, ":") + ":${containerEnv:PATH}"
	}
	searchPath = strings.ReplaceAll(searchPath, "${PATH}", "${containerEnv:PATH}")
	parts := strings.Split(searchPath, "${containerEnv:PATH}")
	for i := range parts {
		parts[i] = shellQuote(parts[i])
	}
	pathExpression := strings.Join(parts, `"$PATH"`)
	script := "umask 077\nexport PATH=" + pathExpression + "\n" +
		"export HOME=/home/agent DISABLE_AUTOUPDATER=1\n" +
		"for binary in " + strings.Join(quoted, " ") + "; do\n" +
		"  command -v \"$binary\" > " + toolchainDirectory + "/\"$binary\".path || :\n" +
		"  (ulimit -f 8; timeout -k 1 8 \"$binary\" --version > " + toolchainDirectory + "/\"$binary\".version 2>/dev/null)\n" +
		"  code=$?\n" +
		"  printf '%s\\n' \"$code\" > " + toolchainDirectory + "/\"$binary\".status || exit 1\n" +
		"done\nprintf '1\\n' > " + toolchainDirectory + "/schema\n"
	probeEnv := []string{"HOME=/home/agent", "DISABLE_AUTOUPDATER=1"}
	for _, kv := range MiseRuntimeEnv {
		value := kv[1]
		if override, ok := containerEnv[kv[0]]; ok {
			value = override
		}
		probeEnv = append(probeEnv, kv[0]+"="+value)
	}
	if _, code, err := exec(ctx, containerID, []string{"sh", "-c", script}, "1001:1001", probeEnv); err != nil || code != 0 {
		return fmt.Errorf("write toolchain inventory")
	}
	seal := "chown -R 0:0 " + toolchainDirectory + " && chmod -R go-w " + toolchainDirectory
	if _, code, err := exec(ctx, containerID, []string{"sh", "-c", seal}, "0:0", nil); err != nil || code != 0 {
		return fmt.Errorf("seal toolchain inventory")
	}
	return nil
}

type imageFileCopier interface {
	CopyFromContainer(context.Context, string, client.CopyFromContainerOptions) (client.CopyFromContainerResult, error)
}

// inspectToolchain pins inspection to the immutable image ID and reads the
// artifact through a stopped temporary container. No entrypoint is executed,
// no workload is interrupted, and archive entries are never extracted on host.
func (p *Provisioner) inspectToolchain(ctx context.Context, image string, bins []string) *ToolchainInventory {
	if len(bins) == 0 {
		return nil
	}
	unknown := unknownToolchain(bins)
	copier, ok := p.docker.(imageFileCopier)
	if !ok {
		return unknown
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	inspected, err := p.docker.ImageInspect(ctx, image)
	if err != nil || inspected.ID == "" {
		return unknown
	}
	unknown.ImageID = inspected.ID
	created, err := p.docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     &container.Config{Image: inspected.ID, User: "1001:1001", Entrypoint: []string{"/bin/true"}, Labels: map[string]string{TempContainerLabelKey: TempContainerLabelValue}},
		HostConfig: &container.HostConfig{NetworkMode: "none", ReadonlyRootfs: true, CapDrop: []string{"ALL"}},
		Name:       tempContainerName(),
	})
	if err != nil {
		return unknown
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := p.docker.ContainerRemove(cleanup, created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
			p.logger.Warn("remove toolchain inspection container", "container_id", created.ID, "error", err)
		}
	}()
	copied, err := copier.CopyFromContainer(ctx, created.ID, client.CopyFromContainerOptions{SourcePath: toolchainDirectory})
	if err != nil {
		return unknown
	}
	defer copied.Content.Close()
	inventory, err := parseToolchainArchive(copied.Content, bins)
	if err != nil {
		return unknown
	}
	inventory.ImageID = inspected.ID
	return inventory
}
