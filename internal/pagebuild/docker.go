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
	"strings"
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

// DockerBuilder compiles in a locked-down container. Image, when set, is the
// pinned image to run (explicit configuration and tests); otherwise Resolver
// finds the image labelled by `crewship page enable`. With neither, the
// builder is not enabled and never starts a container.
type DockerBuilder struct {
	Image    string
	Resolver *ImageResolver
}

// Availability is implemented by builders that can be wired but not enabled.
// Callers check it before recording a build so a refusal leaves no row behind.
type Availability interface {
	Available(context.Context) error
}

func (d *DockerBuilder) image(ctx context.Context) (string, error) {
	if d.Image != "" {
		return d.Image, ValidateImage(d.Image)
	}
	if d.Resolver == nil {
		return "", ErrNotEnabled
	}
	return d.Resolver.Image(ctx)
}

// Available reports whether a build could start now. At most one
// `docker image ls` runs per resolver TTL, however many requests ask.
func (d *DockerBuilder) Available(ctx context.Context) error {
	_, err := d.image(ctx)
	return err
}

// No crew mounts, credentials, network or project-supplied runtime flags.
// The container has its own deadline even if the server process disappears.
func containerArgs(name, image string) []string {
	return []string{"run", "--rm", "--pull=never", "--name", name, "--label", "crewship.pages.build=true", "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--user=1001:1001", "--init", "--memory=1g", "--memory-swap=1g", "--cpus=1", "--pids-limit=128", "--ulimit", "nofile=256:256", "--ulimit", "fsize=16777216:16777216", "--tmpfs", "/work:rw,nosuid,nodev,noexec,size=512m,uid=1001,gid=1001,mode=0700", "--tmpfs", "/tmp:rw,nosuid,nodev,noexec,size=32m,uid=1001,gid=1001,mode=0700", "--log-driver=none", "--env", "NODE_OPTIONS=--max-old-space-size=384", "--env", "NODE_ENV=production", "--workdir", "/work", "--interactive", "--entrypoint", "timeout", image, "--signal=KILL", "120s", "node", "/opt/pages/build.mjs"}
}

func (d *DockerBuilder) Build(ctx context.Context, p *pages.SourceProject) (*Artifact, error) {
	image, err := d.image(ctx)
	if err != nil {
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
	cmd := exec.CommandContext(ctx, "docker", containerArgs(name, image)...)
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
		// Only a labelled image is replaced by `crewship page enable`; a
		// missing explicit image keeps Docker's own message.
		if d.Image == "" && d.Resolver != nil && d.Resolver.Explicit == "" && imageMissing(logs.buffer.String()) {
			// Removed since it was resolved (`crewship page disable`, docker rmi):
			// forget it so the next request looks again instead of failing here.
			d.Resolver.Invalidate()
			return nil, fmt.Errorf("%w (the Pages tools image was removed)", ErrNotEnabled)
		}
		return nil, fmt.Errorf("Page build failed: %s", logs.buffer.String())
	}
	a, err := decodeWorkerArtifact(out, logs)
	if err != nil {
		return nil, err
	}
	a.Toolchain = image
	if _, _, err := a.Encode(); err != nil {
		return nil, err
	}
	return a, nil
}

func decodeWorkerArtifact(out, logs *boundedOutput) (*Artifact, error) {
	// An exit-zero worker can race cancellation; truncation is still an error.
	if out.overflow || logs.overflow {
		return nil, errors.New("Page build exceeded its output limit")
	}
	var a Artifact
	dec := json.NewDecoder(bytes.NewReader(out.buffer.Bytes()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return nil, fmt.Errorf("invalid worker artifact: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, errors.New("multiple worker artifacts")
	}
	if a.ProfileSHA256 != pageprofile.Fingerprint() {
		return nil, errors.New("Pages tools image does not match this server release; run `" + EnableCommand + "` on the server host to install the matching image")
	}
	return &a, nil
}

// imageMissing recognises Docker's refusal to run an absent image under
// --pull=never.
func imageMissing(stderr string) bool {
	return strings.Contains(stderr, "No such image") || strings.Contains(stderr, "Unable to find image")
}

type boundedOutput struct {
	sync.Mutex
	// Keep the buffer private: embedding promotes ReadFrom and lets io.Copy
	// bypass Write, defeating both the output bound and cancellation.
	buffer   bytes.Buffer
	limit    int
	cancel   context.CancelFunc
	overflow bool
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	n := len(p)
	room := b.limit - b.buffer.Len()
	if len(p) > room {
		p = p[:room]
		b.overflow = true
		b.cancel()
	}
	_, _ = b.buffer.Write(p)
	return n, nil
}
