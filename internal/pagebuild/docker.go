package pagebuild

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/pages"
	pageprofile "github.com/crewship-ai/crewship/tools/pages-build"
)

var imageDigest = regexp.MustCompile(`^(?:[a-zA-Z0-9._:/-]+@)?sha256:[a-f0-9]{64}$`)

func ValidateImage(image string) error {
	if !imageDigest.MatchString(image) {
		return errors.New("Page build image must be a pinned local image ID or repository@sha256 digest")
	}
	return nil
}

type DockerBuilder struct{ Image string }

// No crew mounts, credentials, network or project-supplied runtime flags.
// The container has its own deadline even if the server process disappears.
func containerArgs(name, image string) []string {
	return []string{"run", "--rm", "--pull=never", "--name", name, "--label", "crewship.pages.build=true", "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--user=1001:1001", "--init", "--memory=1g", "--memory-swap=1g", "--cpus=1", "--pids-limit=128", "--ulimit", "nofile=256:256", "--ulimit", "fsize=16777216:16777216", "--tmpfs", "/work:rw,nosuid,nodev,noexec,size=512m,uid=1001,gid=1001,mode=0700", "--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=32m,uid=1001,gid=1001,mode=0700", "--log-driver=none", "--env", "NODE_OPTIONS=--max-old-space-size=384", "--env", "NODE_ENV=production", "--workdir", "/work", "--interactive", "--entrypoint", "timeout", image, "--signal=KILL", "120s", "node", "/opt/pages/build.mjs"}
}

func (d *DockerBuilder) Build(ctx context.Context, p *pages.SourceProject) (*Artifact, error) {
	if err := ValidateImage(d.Image); err != nil {
		return nil, err
	}
	if _, err := p.MarshalYAMLSource(); err != nil {
		return nil, err
	}
	if err := pageprofile.ValidateDependencies(p); err != nil {
		return nil, err
	}
	input, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	name := "crewship-page-build-" + rand.Text()
	ctx, cancel := context.WithTimeout(ctx, 130*time.Second)
	defer cancel()
	// Always remove by the unpredictable name: cancellation of an attached
	// Docker client alone does NOT guarantee cancellation of its container.
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = exec.CommandContext(cleanup, "docker", "rm", "--force", name).Run()
	}()
	out := &boundedOutput{limit: MaxArtifactDocumentBytes + 1, cancel: cancel}
	logs := &boundedOutput{limit: 64 << 10, cancel: cancel}
	cmd := exec.CommandContext(ctx, "docker", containerArgs(name, d.Image)...)
	cmd.WaitDelay = 3 * time.Second
	cmd.Stdin = bytes.NewReader(input)
	cmd.Stdout = out
	cmd.Stderr = logs
	if err := cmd.Run(); err != nil {
		if out.overflow || logs.overflow {
			return nil, errors.New("Page build exceeded its output limit")
		}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("Page build stopped: %w", ctx.Err())
		}
		return nil, fmt.Errorf("Page build failed: %s", logs.String())
	}
	var a Artifact
	dec := json.NewDecoder(bytes.NewReader(out.Bytes()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return nil, fmt.Errorf("invalid worker artifact: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, errors.New("multiple worker artifacts")
	}
	if a.ProfileSHA256 != pageprofile.Fingerprint() {
		return nil, errors.New("Pages tools image does not match this server release; rebuild and install the image from tools/pages-build")
	}
	a.Toolchain = d.Image
	if _, _, err := a.Encode(); err != nil {
		return nil, err
	}
	return &a, nil
}

type boundedOutput struct {
	sync.Mutex
	bytes.Buffer
	limit    int
	cancel   context.CancelFunc
	overflow bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	n := len(p)
	room := b.limit - b.Len()
	if len(p) > room {
		p = p[:room]
		b.overflow = true
		b.cancel()
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}
