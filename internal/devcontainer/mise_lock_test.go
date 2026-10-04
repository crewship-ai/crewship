package devcontainer

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestMiseLockInputIsNotSilentlyDiscarded(t *testing.T) {
	input := `{"tools":{"node":"22.0.0"},"lock":{"schema_version":1,"files":{"mise.lock":"lockfile_version = 3\n"}}}`
	cfg, err := ParseMiseConfig(input)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"mise.lock"`) {
		t.Fatalf("dependency lock silently dropped: %s", raw)
	}
	var calls []string
	err = InstallMiseTools(context.Background(), "fixture", cfg, func(_ context.Context, _ string, cmd []string, _ string, _ []string) (string, int, error) {
		calls = append(calls, strings.Join(cmd, " "))
		return "", 0, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "--locked") || !strings.Contains(joined, "mise.lock") {
		t.Fatalf("lock was not installed/enforced: %s", joined)
	}
}

func TestMiseLockRejectsTraversalBeforeAnyExec(t *testing.T) {
	cfg, err := ParseMiseConfig(`{"tools":{"node":"22"},"lock":{"schema_version":1,"files":{"mise.lock":"x","../../escape":"payload"}}}`)
	if err != nil {
		return
	}
	calls := 0
	err = InstallMiseTools(context.Background(), "fixture", cfg, func(_ context.Context, _ string, _ []string, _ string, _ []string) (string, int, error) {
		calls++
		return "", 0, nil
	})
	if err == nil || calls != 0 {
		t.Fatalf("invalid lock reached execution: err=%v calls=%d", err, calls)
	}
}

func TestMiseLockBundleValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*MiseLockBundle)
	}{
		{"schema", func(b *MiseLockBundle) { b.SchemaVersion = 2 }},
		{"missing", func(b *MiseLockBundle) { delete(b.Files, "mise.lock") }},
		{"absolute", func(b *MiseLockBundle) { b.Files["/tmp/payload"] = "x" }},
		{"normalization", func(b *MiseLockBundle) { b.Files[".mise/locks/a/../x"] = "x" }},
		{"backslash", func(b *MiseLockBundle) { b.Files[`.mise/locks/a\b`] = "x" }},
		{"shell", func(b *MiseLockBundle) { b.Files[".mise/locks/a/$(id)"] = "x" }},
		{"nul", func(b *MiseLockBundle) { b.Files["mise.lock"] = "\x00" }},
		{"binary", func(b *MiseLockBundle) { b.Files["mise.lock"] = string([]byte{255}) }},
		{"large file", func(b *MiseLockBundle) { b.Files["mise.lock"] = strings.Repeat("x", maxMiseLockFileBytes+1) }},
		{"large bundle", func(b *MiseLockBundle) {
			for i := 0; i < 5; i++ {
				b.Files[fmt.Sprintf(".mise/locks/a/file%d", i)] = strings.Repeat("x", maxMiseLockFileBytes)
			}
		}},
		{"many", func(b *MiseLockBundle) {
			for i := 0; i < maxMiseLockFiles; i++ {
				b.Files[fmt.Sprintf(".mise/locks/a/file%d", i)] = "x"
			}
		}},
		{"file parent", func(b *MiseLockBundle) {
			b.Files[".mise/locks/a/file"] = "x"
			b.Files[".mise/locks/a/file/child"] = "y"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &MiseLockBundle{SchemaVersion: 1, Files: map[string]string{"mise.lock": "lockfile_version = 3\n"}}
			tc.mutate(b)
			if err := b.Validate(); err == nil {
				t.Fatal("invalid bundle accepted")
			}
		})
	}
}

func TestMiseLockIsPreservedByAdapterPlanningAndChangesBuildIdentity(t *testing.T) {
	input := `{"tools":{"node":"22"},"lock":{"schema_version":1,"files":{"mise.lock":"original"}}}`
	plan, err := EnsureAdapterCLIs(&Config{}, input, []string{"CODEX_CLI"})
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := ParseMiseConfig(plan.MiseConfig)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Lock == nil || cfg.Lock.Files["mise.lock"] != "original" || cfg.Tools["codex"] != "latest" {
		t.Fatalf("plan=%s", plan.MiseConfig)
	}
	changed := strings.Replace(input, "original", "replacement", 1)
	if configHash("base", &Config{}, input, "") == configHash("base", &Config{}, changed, "") {
		t.Fatal("changed lock reused old build identity")
	}
	reordered := `{"lock":{"files":{"mise.lock":"original"},"schema_version":1},"tools":{"node":"22"}}`
	if configHash("base", &Config{}, input, "") != configHash("base", &Config{}, reordered, "") {
		t.Fatal("equivalent bundle changed build identity")
	}
}

func TestMiseLockRecorderEnforcesInstallAndBoundsArguments(t *testing.T) {
	b := &MiseLockBundle{SchemaVersion: 1, Files: map[string]string{"mise.lock": strings.Repeat("x", maxMiseLockFileBytes), ".mise/locks/gemini-cli/1/aube-lock.yaml": "lock: preserved\n"}}
	cfg := &MiseConfig{Tools: map[string]string{"node": "22"}, Lock: b}
	rec := &dockerfileRecorder{}
	calls := 0
	err := InstallMiseTools(context.Background(), "", cfg, func(ctx context.Context, id string, args []string, user string, env []string) (string, int, error) {
		calls++
		for _, arg := range args {
			if len(arg) > 40<<10 {
				t.Fatal("lock exceeded bounded argument size")
			}
		}
		return rec.exec(ctx, id, args, user, env)
	})
	if err != nil {
		t.Fatal(err)
	}
	steps := strings.Join(rec.steps(), "\n")
	for _, want := range []string{"--locked", "--force", "aube-lock.yaml", "USER 1001:1001"} {
		if !strings.Contains(steps, want) {
			t.Fatalf("recorded build missing %q", want)
		}
	}
	if calls < 10 {
		t.Fatal("large lock was not chunked")
	}
}

func TestMiseLockWriteFailureNeverFallsBackToUnlockedInstall(t *testing.T) {
	cfg := &MiseConfig{Tools: map[string]string{"node": "22"}, Lock: &MiseLockBundle{SchemaVersion: 1, Files: map[string]string{"mise.lock": "x"}}}
	installed := false
	err := InstallMiseTools(context.Background(), "", cfg, func(_ context.Context, _ string, args []string, _ string, _ []string) (string, int, error) {
		if args[0] == "mise" {
			installed = true
		}
		if strings.Contains(strings.Join(args, " "), "mise.lock") {
			return "PRIVATE_DIAGNOSTIC", 1, nil
		}
		return "", 0, nil
	})
	if err == nil || installed || strings.Contains(err.Error(), "PRIVATE_DIAGNOSTIC") {
		t.Fatalf("unsafe fallback/diagnostic: err=%v install=%v", err, installed)
	}
}
