package devcontainer

import (
	"archive/tar"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// recordingBuilder captures what the provisioner asked to be built.
type recordingBuilder struct {
	available  bool
	calls      int
	tag        string
	dockerfile string
	err        error
}

func (b *recordingBuilder) Available() bool { return b.available }

func (b *recordingBuilder) Build(_ context.Context, contextDir, tag string, _ func(string)) error {
	b.calls++
	b.tag = tag
	b.dockerfile = readDockerfile(contextDir)
	return b.err
}

func readDockerfile(dir string) string {
	raw, err := readFileBestEffort(dir + "/Dockerfile")
	if err != nil {
		return ""
	}
	return raw
}

func TestDockerfileRecorder_RootCommandBecomesExecFormRun(t *testing.T) {
	r := &dockerfileRecorder{}
	out, code, err := r.exec(context.Background(), "cid", []string{"bash", "-c", "echo hi"}, "0:0", nil)
	if err != nil || code != 0 || out != "" {
		t.Fatalf("recorder must report success and no output, got %q %d %v", out, code, err)
	}
	step := strings.Join(r.steps(), "\n")
	// Exec form, so the layer's own argv is not re-parsed by a shell; the
	// commands inside are single-quoted instead.
	if !strings.HasPrefix(step, `RUN ["sh","-c",`) {
		t.Errorf("expected an exec-form RUN, got:\n%s", step)
	}
	if !strings.Contains(step, `'bash' '-c' '\''echo hi'\''`) && !strings.Contains(step, "echo hi") {
		t.Errorf("command not carried through:\n%s", step)
	}
	// A root step must not switch users — a stray USER would leak into every
	// layer after it.
	if strings.Contains(step, "USER ") {
		t.Errorf("root step should emit no USER directive, got:\n%s", step)
	}
}

// Exec form is not a style choice: shell form would re-interpret the layer's
// argv, so a postCreate containing a quote or a $VAR would either break the
// build or expand at the wrong time. The commands inside the layer are
// single-quoted for the same reason.
func TestDockerfileRecorder_UserCommandSwitchesUserAndRestoresRoot(t *testing.T) {
	r := &dockerfileRecorder{}
	_, _, _ = r.exec(context.Background(), "cid",
		[]string{"bash", "-c", "npm ci"}, "1001:1001",
		[]string{"HOME=/home/agent", "USER=agent"})

	step := strings.Join(r.steps(), "\n")
	for _, want := range []string{
		"USER 1001:1001",
		`RUN ["sh","-c",`,
		"HOME=/home/agent",
		"npm ci",
		"USER root",
	} {
		if !strings.Contains(step, want) {
			t.Errorf("missing %q in:\n%s", want, step)
		}
	}
	if strings.Index(step, "USER 1001:1001") > strings.Index(step, "USER root") {
		t.Error("must switch to the agent user before restoring root")
	}
}

func TestGenerateDockerfile_RendersExtraStepsAfterEnv(t *testing.T) {
	df, err := GenerateDockerfile(DockerfileBuild{
		BaseImage:  "debian:12",
		RootEnv:    map[string]string{"FOO": "bar"},
		ExtraSteps: []string{`RUN ["bash","-c","echo provisioned"]`},
	})
	if err != nil {
		t.Fatalf("GenerateDockerfile: %v", err)
	}
	envAt := strings.Index(df, "ENV FOO=")
	stepAt := strings.Index(df, "echo provisioned")
	if envAt < 0 || stepAt < 0 {
		t.Fatalf("expected both ENV and the extra step, got:\n%s", df)
	}
	if stepAt < envAt {
		t.Error("extra steps must render after the containerEnv ENV lines")
	}
}

func TestProvisionByBuild_BakesEveryStepIntoOneImage(t *testing.T) {
	b := &recordingBuilder{available: true}
	p := NewBuildOnlyProvisioner(b, nil, slog.Default())

	cfg := &Config{
		Image:             "debian:12",
		PostCreateCommand: "echo post-create",
		ContainerEnv:      map[string]string{"CREW": "engineering"},
	}
	res, err := p.ProvisionByBuild(context.Background(), "debian:12", cfg, "[tools]\nnode = \"22\"")
	if err != nil {
		t.Fatalf("ProvisionByBuild: %v", err)
	}
	if b.calls != 1 {
		t.Fatalf("expected exactly one build, got %d", b.calls)
	}
	if res.CachedImage != b.tag {
		t.Errorf("result image %q should be the tag built %q", res.CachedImage, b.tag)
	}

	// Every step the commit path runs in a temp container has to be a layer
	// here instead, or the built image is quietly less provisioned than the
	// committed one.
	for _, want := range []string{
		"chown -R 1001:1001", // agent home ownership
		"mise",               // mise binary + tools
		"echo post-create",   // root-level postCreateCommand
		"/etc/environment",   // aggregated containerEnv
		"/root/.cache",       // cache cleanup
	} {
		if !strings.Contains(b.dockerfile, want) {
			t.Errorf("built Dockerfile is missing %q:\n%s", want, b.dockerfile)
		}
	}
}

// LoginPath is captured by running a login shell inside the finished container.
// A build has no container to run in, and the contract is best-effort — the
// runtime falls back to the well-known devcontainer bin dirs — so it stays
// empty here rather than blocking the path.
func TestProvisionByBuild_LeavesLoginPathToTheRuntimeFallback(t *testing.T) {
	b := &recordingBuilder{available: true}
	p := NewBuildOnlyProvisioner(b, nil, slog.Default())

	res, err := p.ProvisionByBuild(context.Background(), "debian:12", &Config{Image: "debian:12"}, "")
	if err != nil {
		t.Fatalf("ProvisionByBuild: %v", err)
	}
	if res.Requirements.LoginPath != "" {
		t.Errorf("LoginPath must stay empty on the build path, got %q", res.Requirements.LoginPath)
	}
}

func TestProvisionByBuild_RefusesWithoutAnAvailableBuilder(t *testing.T) {
	p := NewBuildOnlyProvisioner(&recordingBuilder{available: false}, nil, slog.Default())
	if _, err := p.ProvisionByBuild(context.Background(), "debian:12", &Config{Image: "debian:12"}, ""); err == nil {
		t.Fatal("an unavailable builder must fail loudly, not build nothing and report success")
	}
}

// Apple's `container build` silently produces an EMPTY directory for
// `COPY <dir>/ <dest>/` — verified against container 1.2.0 on a fresh builder
// with disk to spare: the destination is created, nothing lands in it, and the
// first feature's install.sh then fails with "No such file or directory".
// Single files, tar archives and `ADD <tar>` all work, so features travel as
// archives on this path (#1779).
func TestGenerateDockerfile_FeatureArchivesUseAddNotCopy(t *testing.T) {
	feat := &ResolvedFeature{Ref: "ghcr.io/x/common-utils:1", Dir: t.TempDir()}
	feat.Metadata.ID = "common-utils"

	df, err := GenerateDockerfile(DockerfileBuild{
		BaseImage:       "debian:12",
		Features:        []*ResolvedFeature{feat},
		FeatureArchives: true,
	})
	if err != nil {
		t.Fatalf("GenerateDockerfile: %v", err)
	}
	if !strings.Contains(df, "ADD features/common-utils.tar /tmp/devcontainer-features/common-utils/") {
		t.Errorf("expected an ADD of the feature archive, got:\n%s", df)
	}
	if strings.Contains(df, "COPY features/common-utils/") {
		t.Errorf("the directory COPY must be gone — it produces an empty dir on Apple:\n%s", df)
	}
}

func TestStageBuildContext_WritesFeatureArchiveContainingInstallScript(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "install.sh"), []byte("echo hi\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	feat := &ResolvedFeature{Ref: "ghcr.io/x/common-utils:1", Dir: src}
	feat.Metadata.ID = "common-utils"

	dir, err := stageBuildContextWithSteps("debian:12", []*ResolvedFeature{feat}, nil, nil, nil, "tag:1")
	if err != nil {
		t.Fatalf("stage: %v", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	names := tarEntryNames(t, filepath.Join(dir, featureContextDir, "common-utils.tar"))
	// Archived at the root, so `ADD …/common-utils.tar <dest>/` lands
	// install.sh directly in <dest> — not under a nested directory.
	if !containsString(names, "install.sh") {
		t.Errorf("archive should hold install.sh at its root, got %v", names)
	}
}

func tarEntryNames(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path) // #nosec G304 — test temp path
	if err != nil {
		t.Fatalf("opening feature archive: %v", err)
	}
	defer func() { _ = f.Close() }()
	var names []string
	tr := tar.NewReader(f)
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		names = append(names, h.Name)
	}
	return names
}

func containsString(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}

// Every RUN layer costs ~30-60s of fixed overhead on Apple's builder, measured
// on a real provisioning run: `printf >> /etc/environment` took 136.7s, a bare
// `mise --version` took 50.5s, `mkdir -p` took 76.5s. None of those are slow
// commands — the layer is what is slow. One RUN per exec call meant ~15 layers
// and a ~20 minute build, most of it overhead. Consecutive commands sharing a
// user therefore collapse into a single layer.
func TestDockerfileRecorder_CoalescesConsecutiveStepsIntoOneLayer(t *testing.T) {
	r := &dockerfileRecorder{}
	ctx := context.Background()
	_, _, _ = r.exec(ctx, "", []string{"mkdir", "-p", "/home/agent"}, "0:0", nil)
	_, _, _ = r.exec(ctx, "", []string{"chown", "-R", "1001:1001", "/home/agent"}, "0:0", nil)
	_, _, _ = r.exec(ctx, "", []string{"mise", "--version"}, "0:0", nil)

	df := strings.Join(r.steps(), "\n")
	if n := strings.Count(df, "RUN "); n != 1 {
		t.Errorf("three root commands must become ONE layer, got %d:\n%s", n, df)
	}
	for _, want := range []string{"mkdir", "chown", "mise"} {
		if !strings.Contains(df, want) {
			t.Errorf("collapsed layer lost %q:\n%s", want, df)
		}
	}
}

// A user switch is a real boundary: commands either side run as different
// users, so they cannot share a layer.
func TestDockerfileRecorder_StartsANewLayerWhenTheUserChanges(t *testing.T) {
	r := &dockerfileRecorder{}
	ctx := context.Background()
	_, _, _ = r.exec(ctx, "", []string{"mkdir", "-p", "/x"}, "0:0", nil)
	_, _, _ = r.exec(ctx, "", []string{"npm", "ci"}, "1001:1001", []string{"HOME=/home/agent"})
	_, _, _ = r.exec(ctx, "", []string{"rm", "-rf", "/tmp/x"}, "0:0", nil)

	df := strings.Join(r.steps(), "\n")
	if n := strings.Count(df, "RUN "); n != 3 {
		t.Errorf("two user switches must yield three layers, got %d:\n%s", n, df)
	}
	if !strings.Contains(df, "USER 1001:1001") || !strings.Contains(df, "USER root") {
		t.Errorf("user switches missing:\n%s", df)
	}
}

// Arguments reach the shell through single-quoting, so a value carrying a quote
// or a $VAR must survive verbatim — the commit path hands these to exec
// untouched and never lets a shell near them. Decoding the layer's argv is the
// only honest check: asserting on the raw line would really be testing JSON
// escaping.
func TestDockerfileRecorder_QuotesArgumentsSafely(t *testing.T) {
	r := &dockerfileRecorder{}
	_, _, _ = r.exec(context.Background(), "",
		[]string{"bash", "-c", "echo 'it$HOME' \"x\""}, "0:0", nil)

	script := decodeRunScript(t, r.steps()[0])
	// Single quotes are closed, an escaped quote emitted, then reopened — the
	// only way sh allows a quote inside a single-quoted string.
	if !strings.Contains(script, `'"'"'`) {
		t.Errorf("expected single-quote escaping, got script:\n%s", script)
	}
	// $HOME must stay literal: expanding it here would bake the build-time
	// value into the image.
	if !strings.Contains(script, "it$HOME") {
		t.Errorf("the $VAR must survive verbatim, got script:\n%s", script)
	}
}

// decodeRunScript pulls the shell script out of a `RUN ["sh","-c","…"]` line.
func decodeRunScript(t *testing.T, runLine string) string {
	t.Helper()
	raw, ok := strings.CutPrefix(runLine, "RUN ")
	if !ok {
		t.Fatalf("not a RUN line: %q", runLine)
	}
	var argv []string
	if err := json.Unmarshal([]byte(raw), &argv); err != nil {
		t.Fatalf("decoding RUN argv %q: %v", raw, err)
	}
	if len(argv) != 3 {
		t.Fatalf("expected [sh -c script], got %v", argv)
	}
	return argv[2]
}

// A build that reports success and leaves no image is not a success. Seen for
// real: BuildKit exported and tagged, the provisioner logged "provisioned image
// by build", and the tag was absent from the store minutes later — the runtime's
// image state had wedged and everything written after it was lost. The crew was
// then recorded as provisioned against an image nothing could start.
//
// The tag was already consulted when Build FAILED (a wedged CLI may still have
// produced its image). The opposite case went unchecked, which is the one that
// lies in the dangerous direction: it marks a crew ready (#1779).
func TestProvisionByBuild_FailsWhenTheImageIsNotInTheStore(t *testing.T) {
	b := &probingBuilder{recordingBuilder: recordingBuilder{available: true}, exists: false}
	p := NewBuildOnlyProvisioner(b, nil, slog.Default())
	// The store is given a grace period (see waitForImage); shorten it rather
	// than sit through the real one.
	p.imageWaitTimeout = 200 * time.Millisecond
	p.imageWaitInterval = 10 * time.Millisecond

	_, err := p.ProvisionByBuild(context.Background(), "debian:12", &Config{Image: "debian:12"}, "")
	if err == nil {
		t.Fatal("a build whose image is absent afterwards must not report success")
	}
	if !strings.Contains(err.Error(), "never appeared in the image store") {
		t.Errorf("error should say the image is missing, got %q", err)
	}
}

func TestProvisionByBuild_PassesWhenTheImageIsPresent(t *testing.T) {
	b := &probingBuilder{recordingBuilder: recordingBuilder{available: true}, exists: true}
	p := NewBuildOnlyProvisioner(b, nil, slog.Default())

	if _, err := p.ProvisionByBuild(context.Background(), "debian:12", &Config{Image: "debian:12"}, ""); err != nil {
		t.Fatalf("a build whose image is present must succeed: %v", err)
	}
}

// A builder that cannot answer must not block provisioning: the probe is
// corroboration, and turning "I could not check" into a failure would make
// every non-probing builder unusable.
func TestProvisionByBuild_UnprobableBuilderStillPasses(t *testing.T) {
	b := &recordingBuilder{available: true}
	p := NewBuildOnlyProvisioner(b, nil, slog.Default())

	if _, err := p.ProvisionByBuild(context.Background(), "debian:12", &Config{Image: "debian:12"}, ""); err != nil {
		t.Fatalf("a builder with no ImageExists must still provision: %v", err)
	}
}

type probingBuilder struct {
	recordingBuilder
	exists bool
	probes int
}

func (b *probingBuilder) ImageExists(_ context.Context, _ string) (bool, error) {
	b.probes++
	return b.exists, nil
}

// The post-build check and the build watchdog were in conflict. The watchdog
// kills the CLI as soon as BuildKit reports its export DONE, but the runtime is
// still committing the image to its store at that moment — so asking once,
// immediately, found nothing and failed a build that had in fact succeeded.
// Observed live: three crews failed with "not in the image store" and every one
// of the images was present seconds later.
//
// The tag is therefore waited for, not sampled (#1779).
func TestProvisionByBuild_WaitsForTheImageToAppear(t *testing.T) {
	b := &lateProbingBuilder{
		recordingBuilder: recordingBuilder{available: true},
		appearsAfter:     2, // absent on the first two probes, present on the third
	}
	p := NewBuildOnlyProvisioner(b, nil, slog.Default())
	p.imageWaitTimeout = 5 * time.Second
	p.imageWaitInterval = 10 * time.Millisecond

	if _, err := p.ProvisionByBuild(context.Background(), "debian:12", &Config{Image: "debian:12"}, ""); err != nil {
		t.Fatalf("an image that lands a moment later must still count: %v", err)
	}
	if b.probes < 3 {
		t.Errorf("expected the probe to be retried, got %d attempts", b.probes)
	}
}

// It must still give up: an image that never appears is a real failure, and
// waiting forever would put the crew back to the hang this all started from.
func TestProvisionByBuild_GivesUpWhenTheImageNeverAppears(t *testing.T) {
	b := &lateProbingBuilder{
		recordingBuilder: recordingBuilder{available: true},
		appearsAfter:     1 << 30, // never
	}
	p := NewBuildOnlyProvisioner(b, nil, slog.Default())
	p.imageWaitTimeout = 200 * time.Millisecond
	p.imageWaitInterval = 10 * time.Millisecond

	start := time.Now()
	_, err := p.ProvisionByBuild(context.Background(), "debian:12", &Config{Image: "debian:12"}, "")
	if err == nil {
		t.Fatal("an image that never appears must fail")
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("waited %s — the bound did not apply", time.Since(start))
	}
}

type lateProbingBuilder struct {
	recordingBuilder
	appearsAfter int
	probes       int
}

func (b *lateProbingBuilder) ImageExists(_ context.Context, _ string) (bool, error) {
	b.probes++
	return b.probes > b.appearsAfter, nil
}

// The build-only dispatch sits above Provision's cache short-circuit, so the
// macOS path had no cache lookup at all and rebuilt the identical tag every
// time — measured at ~6 minutes for a crew that had not changed, where the
// Docker path returns immediately. The dispatch comment even claimed the cache
// lookup was shared; it was not (#1779).
func TestProvisionByBuild_ReusesAnImageThatIsAlreadyBuilt(t *testing.T) {
	b := &probingBuilder{recordingBuilder: recordingBuilder{available: true}, exists: true}
	p := NewBuildOnlyProvisioner(b, nil, slog.Default())

	res, err := p.ProvisionByBuild(context.Background(), "debian:12", &Config{Image: "debian:12"}, "")
	if err != nil {
		t.Fatalf("ProvisionByBuild: %v", err)
	}
	if b.calls != 0 {
		t.Errorf("an image already in the store must not be rebuilt, got %d builds", b.calls)
	}
	if res.CachedImage == "" {
		t.Error("a cache hit must still report the image it reused")
	}
}

// A tag that is absent must still build — the short-circuit is a cache, not a
// gate.
func TestProvisionByBuild_BuildsWhenTheImageIsAbsent(t *testing.T) {
	b := &appearingBuilder{recordingBuilder: recordingBuilder{available: true}}
	p := NewBuildOnlyProvisioner(b, nil, slog.Default())

	if _, err := p.ProvisionByBuild(context.Background(), "debian:12", &Config{Image: "debian:12"}, ""); err != nil {
		t.Fatalf("ProvisionByBuild: %v", err)
	}
	if b.calls != 1 {
		t.Errorf("expected exactly one build, got %d", b.calls)
	}
}

// appearingBuilder reports the tag absent until it has built it, which is what
// a real store does.
type appearingBuilder struct {
	recordingBuilder
	built bool
}

func (b *appearingBuilder) Build(ctx context.Context, dir, tag string, onLog func(string)) error {
	err := b.recordingBuilder.Build(ctx, dir, tag, onLog)
	b.built = true
	return err
}

func (b *appearingBuilder) ImageExists(_ context.Context, _ string) (bool, error) {
	return b.built, nil
}
