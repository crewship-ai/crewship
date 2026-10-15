package pagebuild

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	pageprofile "github.com/crewship-ai/crewship/tools/pages-build"
)

func TestDockerBuildPolicy(t *testing.T) {
	args := strings.Join(containerArgs("test", "sha256:"+strings.Repeat("a", 64)), " ")
	for _, flag := range []string{"--network=none", "--read-only", "--cap-drop=ALL", "--user=1001:1001", "--memory=1g", "--memory-swap=1g", "--cpus=1", "--pids-limit=128", "--pull=never", "--log-driver=none", "--signal=KILL 120s"} {
		if !strings.Contains(args, flag) {
			t.Fatalf("missing %s", flag)
		}
	}
	for _, flag := range []string{"--privileged", "--volume", "--mount", "docker.sock"} {
		if strings.Contains(args, flag) {
			t.Fatalf("unexpected %s", flag)
		}
	}
	for _, image := range []string{"node:latest", "", "-v:/host"} {
		if ValidateImage(image) == nil {
			t.Fatalf("unpinned image %q", image)
		}
	}
}
func TestDockerPreviewBuildIntegration(t *testing.T) {
	image := os.Getenv("CREWSHIP_TEST_PAGE_BUILD_IMAGE")
	if image == "" {
		// SKIP-WAIVER(#2472): ordinary Go tests need no image; pages-apps CI builds it and rejects skips.
		t.Skip("set CREWSHIP_TEST_PAGE_BUILD_IMAGE to a locally built pinned tools image")
	}
	builder := &DockerBuilder{Image: image}
	p := pageprofile.Source()
	if main := os.Getenv("CREWSHIP_TEST_PAGE_MAIN"); main != "" {
		content, err := os.ReadFile(main)
		if err != nil {
			t.Fatal(err)
		}
		for i := range p.Files {
			if p.Files[i].Path == "src/main.tsx" {
				p.Files[i].Content = string(content)
			}
		}
	}
	a, err := builder.Build(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(a.JavaScript, "OPERATIONS") || !strings.Contains(a.CSS, "grid") {
		t.Fatal("starter output missing")
	}
	if file := os.Getenv("CREWSHIP_TEST_PAGE_ARTIFACT_OUT"); file != "" {
		b, _, err := a.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("utf8-chunks", func(t *testing.T) {
		source := pageprofile.Source()
		const marker = "Příliš žluťoučký kůň 🐴"
		for i := range source.Files {
			if source.Files[i].Path == "src/main.tsx" {
				source.Files[i].Content = "console.log(" + fmt.Sprintf("%q", marker) + "); export {}"
			}
		}
		input, err := json.Marshal(source)
		if err != nil {
			t.Fatal(err)
		}
		args := containerArgs("crewship-page-utf8-"+rand.Text(), image)
		// Exercise the installed worker itself with every multibyte code point split.
		args = append(args[:len(args)-1], "--input-type=module", "-e", `
import { Readable } from 'node:stream';
const chunks=[]; for await (const chunk of process.stdin) chunks.push(chunk);
const input=Buffer.concat(chunks);
Object.defineProperty(process, 'stdin', {value: Readable.from((async function*(){for(const byte of input) yield Buffer.from([byte])})())});
await import('/opt/pages/build.mjs');`)
		cmd := exec.CommandContext(context.Background(), "docker", args...)
		cmd.Stdin = bytes.NewReader(input)
		var logs bytes.Buffer
		cmd.Stderr = &logs
		output, err := cmd.Output()
		if err != nil {
			t.Fatalf("chunked compiler: %v %s", err, logs.String())
		}
		var artifact Artifact
		if err := json.Unmarshal(output, &artifact); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(artifact.JavaScript, marker) || strings.ContainsRune(artifact.JavaScript, '\ufffd') {
			t.Fatalf("UTF-8 changed in artifact: %s", artifact.JavaScript)
		}
	})
	t.Run("typecheck", func(t *testing.T) {
		bad := pageprofile.Source()
		for i := range bad.Files {
			if bad.Files[i].Path == "src/main.tsx" {
				bad.Files[i].Content = "const value: number = 'invalid'; export { value }"
			}
		}
		if _, err := builder.Build(context.Background(), bad); err == nil {
			t.Fatal("accepted type error")
		}
	})
	t.Run("lockfile", func(t *testing.T) {
		bad := pageprofile.Source()
		for i := range bad.Files {
			if bad.Files[i].Path == "pnpm-lock.yaml" {
				bad.Files[i].Content += "\n# changed\n"
			}
		}
		if _, err := builder.Build(context.Background(), bad); err == nil {
			t.Fatal("accepted different dependency lock")
		}
	})
}

func TestDockerBuilderRejectsStaleCompilerProfile(t *testing.T) {
	directory := t.TempDir()
	// A successful process exit is insufficient: old images emit valid artifacts
	// whose SDK/compiler differs from the server release.
	script := `#!/bin/sh
if [ "$1" = run ]; then
 cat >/dev/null
 printf '%s' '{"format":"crewship-page-preview/v1","javascript":"void 0","css":"","toolchain":"old"}'
fi
`
	if err := os.WriteFile(filepath.Join(directory, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	builder := &DockerBuilder{Image: "sha256:" + strings.Repeat("a", 64)}
	if _, err := builder.Build(context.Background(), pageprofile.Source()); err == nil || !strings.Contains(err.Error(), "does not match this server release") || !strings.Contains(err.Error(), EnableCommand) {
		t.Fatalf("stale compiler not rejected: %v", err)
	}
}

// fakeDocker puts a `docker` on PATH that records every invocation and, for
// `run`, prints stderr and exits 1. It never reaches a daemon.
func fakeDocker(t *testing.T, runStderr string) (calls func() string) {
	t.Helper()
	directory := t.TempDir()
	log := filepath.Join(directory, "calls")
	script := `#!/bin/sh
echo "$@" >>'` + log + `'
if [ "$1" = run ]; then
 cat >/dev/null
 echo '` + runStderr + `' >&2
 exit 1
fi
`
	if err := os.WriteFile(filepath.Join(directory, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() string {
		b, _ := os.ReadFile(log)
		return string(b)
	}
}

func TestDockerBuilderImageSelection(t *testing.T) {
	explicit := "sha256:" + strings.Repeat("c", 64)
	labelled := "sha256:" + strings.Repeat("d", 64)
	for _, tc := range []struct {
		name       string
		builder    func(lookups *int) *DockerBuilder
		notEnabled bool
		lookups    int
		image      string
	}{
		{name: "no image and no resolver", builder: func(*int) *DockerBuilder { return &DockerBuilder{} }, notEnabled: true},
		{name: "explicit image wins over label", lookups: 0, image: explicit, builder: func(n *int) *DockerBuilder {
			return &DockerBuilder{Image: explicit, Resolver: &ImageResolver{Lookup: func(context.Context) ([]string, error) { *n++; return []string{labelled}, nil }}}
		}},
		{name: "resolver explicit", image: explicit, builder: func(n *int) *DockerBuilder {
			return &DockerBuilder{Resolver: &ImageResolver{Explicit: explicit, Lookup: func(context.Context) ([]string, error) { *n++; return nil, nil }}}
		}},
		{name: "labelled image enables", lookups: 1, image: labelled, builder: func(n *int) *DockerBuilder {
			return &DockerBuilder{Resolver: &ImageResolver{Lookup: func(context.Context) ([]string, error) { *n++; return []string{labelled}, nil }}}
		}},
		// One lookup; the short negative TTL, not a second lookup, lets
		// enable take effect without a restart.
		{name: "no labelled image", notEnabled: true, lookups: 1, builder: func(n *int) *DockerBuilder {
			return &DockerBuilder{Resolver: &ImageResolver{Lookup: func(context.Context) ([]string, error) { *n++; return nil, nil }}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := fakeDocker(t, "compile error")
			lookups := 0
			b := tc.builder(&lookups)
			err := b.Available(t.Context())
			if errors.Is(err, ErrNotEnabled) != tc.notEnabled || (!tc.notEnabled && err != nil) {
				t.Fatalf("Available: %v", err)
			}
			if lookups != tc.lookups {
				t.Fatalf("lookups = %d, want %d", lookups, tc.lookups)
			}
			_, err = b.Build(t.Context(), pageprofile.Source())
			if tc.notEnabled {
				if !errors.Is(err, ErrNotEnabled) {
					t.Fatalf("Build: %v", err)
				}
				// Not enabled means no container is ever started.
				if strings.Contains(calls(), "run") {
					t.Fatalf("docker run invoked while not enabled: %s", calls())
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), "compile error") {
				t.Fatalf("Build: %v", err)
			}
			if !strings.Contains(calls(), "run ") || !strings.Contains(calls(), " "+tc.image+" ") {
				t.Fatalf("docker run did not use %s: %s", tc.image, calls())
			}
		})
	}
}

func TestDockerBuilderForgetsRemovedImage(t *testing.T) {
	labelled := "sha256:" + strings.Repeat("d", 64)
	fakeDocker(t, "docker: Error response from daemon: No such image: "+labelled+".")
	lookups := 0
	resolver := &ImageResolver{Lookup: func(context.Context) ([]string, error) { lookups++; return []string{labelled}, nil }}
	_, err := (&DockerBuilder{Resolver: resolver}).Build(t.Context(), pageprofile.Source())
	if !errors.Is(err, ErrNotEnabled) {
		t.Fatalf("removed image not reported as not enabled: %v", err)
	}
	if _, err := resolver.Image(t.Context()); err != nil || lookups != 2 {
		t.Fatalf("resolver not invalidated: %v, %d lookups", err, lookups)
	}
}

func TestWorkerOutputOverflowAfterSuccessfulExit(t *testing.T) {
	for _, stderr := range []bool{false, true} {
		out := &boundedOutput{limit: 1, cancel: func() {}}
		logs := &boundedOutput{limit: 1, cancel: func() {}}
		target := out
		if stderr {
			target = logs
		}
		_, _ = target.Write([]byte("{}"))
		if _, err := decodeWorkerArtifact(out, logs); err == nil || err.Error() != "Page build exceeded its output limit" {
			t.Fatalf("overflow diagnosis: %v", err)
		}
	}
}
