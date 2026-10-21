package mooring

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"sync/atomic"
	"time"
)

var imageDigestPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9./:_-]*@sha256:[a-f0-9]{64}$`)

// DockerRunner accepts only an operator-provided immutable image reference. It
// never builds/pulls an image, mounts host paths, or supplies host credentials.
type DockerRunner struct {
	Image string
	state *dockerRuntimeState
}
type dockerRuntimeState struct{ blocked atomic.Bool }

func NewDockerRunner(image string) (DockerRunner, error) {
	if !imageDigestPattern.MatchString(image) {
		return DockerRunner{}, ErrInvalid
	}
	return DockerRunner{Image: image, state: &dockerRuntimeState{}}, nil
}
func (r DockerRunner) Run(ctx context.Context, input []byte, c Command) (result EngineResult, err error) {
	if r.state == nil {
		return EngineResult{}, ErrInvalid
	}
	if r.state.blocked.Load() {
		return EngineResult{}, ErrEngineUnavailable
	}
	if !imageDigestPattern.MatchString(r.Image) {
		return EngineResult{}, ErrInvalid
	}
	if err := c.validate(); err != nil {
		return EngineResult{}, err
	}
	if len(input) > MaxSnapshotBytes {
		return EngineResult{}, ErrInvalid
	}
	body, err := json.Marshal(WorkerRequest{input, c})
	if err != nil {
		return EngineResult{}, err
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return EngineResult{}, err
	}
	name := "crewship-gridcraft-" + hex.EncodeToString(nonce[:])
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// Docker's client can die before the container. Always clean exactly this
		// random owned name, including ambiguous create/cancellation outcomes.
		if cleanupErr := exec.CommandContext(cleanup, "docker", "rm", "--force", name).Run(); cleanupErr != nil {
			r.state.blocked.Store(true)
			result = EngineResult{}
			err = fmt.Errorf("%w: cleanup unconfirmed", ErrEngineUnavailable)
		}
	}()
	bounded, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	args := []string{"run", "--interactive", "--pull=never", "--name", name, "--label", "crewship.mooring-worker=" + name,
		"--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges:true",
		"--user=1001:1001", "--memory=256m", "--memory-swap=256m", "--cpus=1", "--pids-limit=32",
		"--tmpfs=/scratch:rw,noexec,nosuid,nodev,size=32m,mode=0700,uid=1001,gid=1001",
		"--env=HOME=/scratch", "--env=TMPDIR=/scratch", "--workdir=/scratch",
		"--entrypoint=/usr/local/bin/crewship-gridcraft-worker", r.Image}
	cmd := exec.CommandContext(bounded, "docker", args...)
	cmd.Stdin = bytes.NewReader(body)
	cmd.WaitDelay = 2 * time.Second
	output := &boundedOutput{remaining: 2*MaxSnapshotBytes + 4096}
	cmd.Stdout = output
	if err = cmd.Run(); err != nil {
		return EngineResult{}, fmt.Errorf("%w: worker execution failed", ErrEngineUnavailable)
	}
	if err = DecodeStrict(output.Bytes(), &result); err != nil {
		return EngineResult{}, err
	}
	return result, nil
}

type boundedOutput struct {
	buffer    bytes.Buffer
	remaining int
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(p) > b.remaining {
		return 0, errors.New("engine output limit exceeded")
	}
	n, err := b.buffer.Write(p)
	b.remaining -= n
	return n, err
}

func (b *boundedOutput) Bytes() []byte { return b.buffer.Bytes() }
