package testutil

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
)

const normalHelperPackage = "./internal/testutil/cmd/migratedtemplate"

// Nothing is prepared before the requesting fixture starts: source hashing,
// helper compilation, verification and full Migrate all belong to that call.
func buildNormalMigratedTemplate(path string) (bool, error) {
	plan, reason, err := normalTemplatePlan()
	if err != nil {
		return true, err
	}
	if reason != "" {
		fmt.Fprintln(os.Stderr, "testutil: normal fixture helper unavailable ("+reason+"); running full migrations in this process")
		return false, nil
	}
	root, err := fixtureRoot()
	if err != nil {
		return true, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	return true, plan.buildFixture(ctx, root, path)
}

func (p templateHelperPlan) buildFixture(ctx context.Context, root, path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.ContainsAny(path, "?\x00#") {
		return fmt.Errorf("fixture output must be a clean absolute file path")
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if _, err := os.Lstat(path + suffix); err == nil {
			return fmt.Errorf("fixture output or sidecar already exists: %s", path+suffix)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	helper, err := p.cachedHelper(ctx, root)
	if err != nil {
		return err
	}
	if err = p.checkSource(); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(filepath.Dir(path), ".normal-template-building-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	stagedPath := filepath.Join(stage, "template.db")
	out, err := p.execute(ctx, p.moduleRoot, p.env, helper, "--output", stagedPath, "--fingerprint", p.fingerprint, "--go-version", runtime.Version())
	if err != nil {
		return fmt.Errorf("build migrated fixture with normal helper: %w", err)
	}
	if err = p.checkReceipt(out); err != nil {
		return err
	}
	info, err := os.Lstat(stagedPath)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 || !fixtureRootOwned(info) {
		return fmt.Errorf("normal helper did not produce a private database file")
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Lstat(stagedPath + suffix); !os.IsNotExist(err) {
			return fmt.Errorf("normal helper did not checkpoint and close its database: %s", suffix)
		}
	}
	if err = os.Link(stagedPath, path); err != nil {
		return fmt.Errorf("publish normal template without replacement: %w", err)
	}
	return nil
}

type templateHelperPlan struct {
	moduleRoot  string
	goTool      string
	fingerprint string
	sourceKey   string
	cacheKey    string
	tags        string
	env         []string
	// Per-call seams keep failure/cache tests isolated; no mutable global hooks.
	execute        func(context.Context, string, []string, string, ...string) ([]byte, error)
	sourceIdentity func(string) (string, error)
}

func normalTemplatePlan() (templateHelperPlan, string, error) {
	root, err := findModuleRoot()
	if err != nil {
		return templateHelperPlan{}, "module source tree is absent", nil
	}
	source := filepath.Join(root, "internal", "testutil", "cmd", "migratedtemplate", "main.go")
	if info, err := os.Lstat(source); err != nil || !info.Mode().IsRegular() {
		return templateHelperPlan{}, "fixture helper source is absent", nil
	}
	// An absent build SDK deliberately selects the full in-process fallback.
	// A different Go found on PATH could compile different migration semantics.
	sdkRoot := runtime.GOROOT() //nolint:staticcheck // The fixture intentionally requires the running test binary's SDK, with an explicit missing-SDK fallback.
	tool := filepath.Join(sdkRoot, "bin", "go")
	if runtime.GOOS == "windows" {
		tool += ".exe"
	}
	if info, err := os.Stat(tool); err != nil || !info.Mode().IsRegular() {
		return templateHelperPlan{}, "the running binary's Go SDK is absent", nil
	}
	for name, want := range map[string]string{"GOOS": runtime.GOOS, "GOARCH": runtime.GOARCH} {
		if configured := os.Getenv(name); configured != "" && configured != want {
			return templateHelperPlan{}, "Go target differs from the running test binary", nil
		}
	}
	fingerprint, err := database.MigrationFingerprint()
	if err != nil {
		return templateHelperPlan{}, "", err
	}
	sourceKey, err := sharedTemplateKey(root)
	if err != nil {
		return templateHelperPlan{}, "", err
	}
	tags := ""
	settings := map[string]string{}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			settings[setting.Key] = setting.Value
			if setting.Key == "-tags" {
				tags = setting.Value
			}
		}
	}
	identity := struct {
		Format, Fingerprint, Source, GoTool, GoVersion, GOOS, GOARCH, GOFLAGS, Tags string
		Settings                                                                    map[string]string
	}{"normal-helper-v1", fingerprint, sourceKey, tool, runtime.Version(), runtime.GOOS, runtime.GOARCH, os.Getenv("GOFLAGS"), tags, settings}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return templateHelperPlan{}, "", err
	}
	hash := sha256.Sum256(encoded)
	// Use this SDK, without automatic switching/downloading a different one.
	// Preserve GOFLAGS verbatim; the command overrides only race instrumentation.
	env := replaceNormalHelperEnv(os.Environ(), "GOROOT", sdkRoot)
	env = replaceNormalHelperEnv(env, "GOTOOLCHAIN", "local")
	return templateHelperPlan{moduleRoot: root, goTool: tool, fingerprint: fingerprint, sourceKey: sourceKey, cacheKey: hex.EncodeToString(hash[:]), tags: tags, env: env, execute: runNormalHelperCommand, sourceIdentity: sharedTemplateKey}, "", nil
}

func replaceNormalHelperEnv(env []string, name, value string) []string {
	prefix := name + "="
	out := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			out = append(out, entry)
		}
	}
	return append(out, prefix+value)
}

func (p templateHelperPlan) checkSource() error {
	current, err := p.sourceIdentity(p.moduleRoot)
	if err != nil {
		return fmt.Errorf("recheck fixture helper source: %w", err)
	}
	if current != p.sourceKey {
		return fmt.Errorf("fixture helper source changed during compilation or before migration; refusing mismatched cache publication")
	}
	return nil
}

func validNormalHelper(path string) (bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 || !fixtureRootOwned(info) || runtime.GOOS != "windows" && info.Mode().Perm()&0100 == 0 {
		return false, fmt.Errorf("normal fixture helper must be an owned private executable: %s", path)
	}
	return true, nil
}

func (p templateHelperPlan) cachedHelper(ctx context.Context, root string) (string, error) {
	unlock, err := lockNormalHelperCache(ctx, filepath.Join(root, ".normal-helper.lock"))
	if err != nil {
		return "", err
	}
	defer unlock()
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	published := filepath.Join(root, "normal-helper-v1-"+p.cacheKey+suffix)
	if ready, err := validNormalHelper(published); ready || err != nil {
		return published, err
	}
	stage, err := os.MkdirTemp(root, "normal-helper-building-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	binary := filepath.Join(stage, "helper"+suffix)
	args := []string{"build", "-race=false"}
	if p.tags != "" {
		args = append(args, "-tags", p.tags)
	}
	args = append(args, "-o", binary, normalHelperPackage)
	if _, err = p.execute(ctx, p.moduleRoot, p.env, p.goTool, args...); err != nil {
		return "", fmt.Errorf("compile normal fixture helper: %w", err)
	}
	if err = os.Chmod(binary, 0500); err != nil {
		return "", err
	}
	out, err := p.execute(ctx, p.moduleRoot, p.env, binary, "--check", "--fingerprint", p.fingerprint, "--go-version", runtime.Version())
	if err != nil {
		return "", fmt.Errorf("verify normal fixture helper: %w", err)
	}
	if err = p.checkReceipt(out); err != nil {
		return "", err
	}
	if err = p.checkSource(); err != nil {
		return "", err
	}
	// A hard link publishes atomically without replacing any existing file.
	// The staging directory is ours alone and is removed on every exit path.
	if err = os.Link(binary, published); err != nil {
		return "", fmt.Errorf("publish normal fixture helper without replacement: %w", err)
	}
	return published, nil
}

func (p templateHelperPlan) checkReceipt(raw []byte) error {
	var receipt struct {
		Fingerprint string `json:"fingerprint"`
		GoVersion   string `json:"go_version"`
		RaceEnabled *bool  `json:"race_enabled"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&receipt); err != nil {
		return fmt.Errorf("invalid normal fixture helper receipt: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return fmt.Errorf("normal fixture helper returned trailing output")
	}
	if receipt.Fingerprint != p.fingerprint || receipt.GoVersion != runtime.Version() || receipt.RaceEnabled == nil || *receipt.RaceEnabled {
		return fmt.Errorf("normal fixture helper receipt does not match the test binary")
	}
	return nil
}

type normalHelperOutput struct {
	bytes.Buffer
	limit int
}

func (b *normalHelperOutput) Write(p []byte) (int, error) {
	size := len(p)
	if b.Len() < b.limit {
		remaining := b.limit - b.Len()
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.Buffer.Write(p)
	}
	return size, nil
}

func runNormalHelperCommand(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = env
	isolateNormalHelperCommand(cmd)
	stdout := &normalHelperOutput{limit: 4096}
	stderr := &normalHelperOutput{limit: 8192}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}
