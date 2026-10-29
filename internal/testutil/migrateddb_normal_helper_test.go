package testutil

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func helperControlPlan(t *testing.T, root string) (templateHelperPlan, *atomic.Int32) {
	t.Helper()
	count := &atomic.Int32{}
	p := templateHelperPlan{moduleRoot: root, goTool: "test-go-sdk", fingerprint: "compiled-migration", sourceKey: "stable-source", cacheKey: "control-cache", tags: "fixture_tag", env: []string{"GOFLAGS=-p=2 -race -tags=fixture_tag"}, sourceIdentity: func(string) (string, error) { return "stable-source", nil }}
	p.execute = func(_ context.Context, _ string, env []string, name string, args ...string) ([]byte, error) {
		if name == p.goTool {
			count.Add(1)
			if len(args) < 3 || args[0] != "build" || args[1] != "-race=false" {
				t.Error("normal helper did not explicitly override race instrumentation")
			}
			if !reflect.DeepEqual(env, p.env) {
				t.Error("other build environment flags were dropped")
			}
			index := -1
			for i, arg := range args {
				if arg == "-o" {
					index = i + 1
				}
			}
			if index < 0 || index >= len(args) {
				return nil, errors.New("missing private output")
			}
			return nil, os.WriteFile(args[index], []byte("compiler artifact for cache control only"), 0600)
		}
		return json.Marshal(map[string]any{"fingerprint": p.fingerprint, "go_version": runtime.Version(), "race_enabled": false})
	}
	return p, count
}

func TestNormalTemplateHelperCacheAtomicReuseAndBuildFlags(t *testing.T) {
	root := t.TempDir()
	p, count := helperControlPlan(t, root)
	var wg sync.WaitGroup
	paths := make(chan string, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); path, err := p.cachedHelper(t.Context(), root); paths <- path; errs <- err }()
	}
	wg.Wait()
	close(paths)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	want := ""
	for path := range paths {
		if want == "" {
			want = path
		}
		if path != want {
			t.Fatal("same source produced different cache entries")
		}
	}
	if count.Load() != 1 {
		t.Fatalf("compiled %d helpers, want one", count.Load())
	}
	if ok, err := validNormalHelper(want); !ok || err != nil {
		t.Fatalf("published helper invalid: %v", err)
	}
	stages, err := filepath.Glob(filepath.Join(root, "normal-helper-building-*"))
	if err != nil || len(stages) != 0 {
		t.Fatalf("staging files remain: %v %v", stages, err)
	}
}

func TestNormalTemplateHelperFailuresNeverPublishOrRemoveExistingFixture(t *testing.T) {
	for _, failure := range []string{"compile", "receipt", "source-change"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			fixture := filepath.Join(root, "existing-fixture.db")
			original := []byte("existing fixture must survive")
			if err := os.WriteFile(fixture, original, 0600); err != nil {
				t.Fatal(err)
			}
			p, _ := helperControlPlan(t, root)
			execute := p.execute
			p.execute = func(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error) {
				raw, err := execute(ctx, dir, env, name, args...)
				if name == p.goTool && failure == "compile" {
					return nil, errors.New("deliberate compiler failure after partial output")
				}
				if name != p.goTool && failure == "receipt" {
					return []byte(`{"fingerprint":"different","go_version":"wrong","race_enabled":true}`), nil
				}
				return raw, err
			}
			if failure == "source-change" {
				p.sourceIdentity = func(string) (string, error) { return "new-source", nil }
			}
			if _, err := p.cachedHelper(t.Context(), root); err == nil {
				t.Fatal("genuine build/verification/source failure was hidden")
			}
			if raw, err := os.ReadFile(fixture); err != nil || !reflect.DeepEqual(raw, original) {
				t.Fatal("unrelated fixture changed")
			}
			published, _ := filepath.Glob(filepath.Join(root, "normal-helper-v1-*"))
			stages, _ := filepath.Glob(filepath.Join(root, "normal-helper-building-*"))
			if len(published) != 0 || len(stages) != 0 {
				t.Fatalf("failed helper was published or staging leaked: %v %v", published, stages)
			}
		})
	}
}

func TestNormalTemplateHelperRejectsExistingUnsafeCacheWithoutDeletingIt(t *testing.T) {
	root := t.TempDir()
	p, _ := helperControlPlan(t, root)
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	path := filepath.Join(root, "normal-helper-v1-"+p.cacheKey+suffix)
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.cachedHelper(t.Context(), root); err == nil {
		t.Fatal("empty cache artifact was trusted")
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatal("existing cache artifact was deleted")
	}
}

func TestNormalTemplateHelperReceiptRequiresClosedNonRaceIdentity(t *testing.T) {
	p, _ := helperControlPlan(t, t.TempDir())
	for _, raw := range []string{`{}`, `{"fingerprint":"compiled-migration","go_version":"` + runtime.Version() + `"}`, `{"fingerprint":"compiled-migration","go_version":"` + runtime.Version() + `","race_enabled":true}`, `{"fingerprint":"compiled-migration","go_version":"` + runtime.Version() + `","race_enabled":false,"extra":1}`, `{"fingerprint":"compiled-migration","go_version":"` + runtime.Version() + `","race_enabled":false} {}`} {
		if p.checkReceipt([]byte(raw)) == nil {
			t.Errorf("invalid helper receipt accepted: %s", raw)
		}
	}
}

func TestNormalTemplateHelperCrossProcessCache(t *testing.T) {
	root := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errorsCh := make(chan error, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cmd := exec.Command(exe, "-test.run=^TestNormalTemplateHelperCacheWorker$", "-test.count=1")
			cmd.Env = append(os.Environ(), "CREWSHIP_TEMPLATE_CACHE_WORKER="+root)
			out, err := cmd.CombinedOutput()
			if err != nil {
				errorsCh <- errors.New(string(out))
			}
		}()
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "compile-count"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(raw), "compile\n") != 1 {
		t.Fatalf("cross-process cache compiled more than once: %q", raw)
	}
}

func TestNormalTemplateHelperCacheWorker(t *testing.T) {
	root := os.Getenv("CREWSHIP_TEMPLATE_CACHE_WORKER")
	if root == "" {
		return
	}
	p, _ := helperControlPlan(t, root)
	execute := p.execute
	p.execute = func(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error) {
		if name == p.goTool {
			f, err := os.OpenFile(filepath.Join(root, "compile-count"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				return nil, err
			}
			_, err = f.WriteString("compile\n")
			closeErr := f.Close()
			if err != nil {
				return nil, err
			}
			if closeErr != nil {
				return nil, closeErr
			}
		}
		return execute(ctx, dir, env, name, args...)
	}
	if _, err := p.cachedHelper(t.Context(), root); err != nil {
		t.Fatal(err)
	}
}

func TestNormalTemplateHelperPartialMigrationNeverPublishesOrLeaksStage(t *testing.T) {
	root := t.TempDir()
	p, _ := helperControlPlan(t, root)
	execute := p.execute
	p.execute = func(ctx context.Context, dir string, env []string, name string, args ...string) ([]byte, error) {
		for i, arg := range args {
			if arg == "--output" {
				if err := os.WriteFile(args[i+1], []byte("interrupted migration output"), 0600); err != nil {
					return nil, err
				}
				return nil, errors.New("deliberate interrupted migration")
			}
		}
		return execute(ctx, dir, env, name, args...)
	}
	target := filepath.Join(root, "template.db")
	if err := p.buildFixture(t.Context(), root, target); err == nil {
		t.Fatal("partial migration was acknowledged")
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatal("partial fixture was published")
	}
	stages, _ := filepath.Glob(filepath.Join(root, ".normal-template-building-*"))
	if len(stages) != 0 {
		t.Fatal("interrupted helper leaked its owned staging directory")
	}
}

func TestNormalTemplateHelperNeverReplacesExistingOutputOrSidecars(t *testing.T) {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		t.Run("existing"+suffix, func(t *testing.T) {
			root := t.TempDir()
			p, count := helperControlPlan(t, root)
			target := filepath.Join(root, "template.db")
			marker := []byte("existing fixture data")
			if err := os.WriteFile(target+suffix, marker, 0600); err != nil {
				t.Fatal(err)
			}
			if p.buildFixture(t.Context(), root, target) == nil {
				t.Fatal("existing output accepted")
			}
			if count.Load() != 0 {
				t.Fatal("existing-output refusal compiled a helper")
			}
			got, err := os.ReadFile(target + suffix)
			if err != nil || !reflect.DeepEqual(got, marker) {
				t.Fatal("existing fixture changed")
			}
		})
	}
}

func TestNormalTemplateHelperRechecksSourceBeforeCachedHelperRequest(t *testing.T) {
	root := t.TempDir()
	p, _ := helperControlPlan(t, root)
	cached, err := p.cachedHelper(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(cached)
	if err != nil {
		t.Fatal(err)
	}
	p.sourceIdentity = func(string) (string, error) { return "source changed after helper publication", nil }
	target := filepath.Join(root, "template.db")
	if p.buildFixture(t.Context(), root, target) == nil {
		t.Fatal("old helper was requested under changed source identity")
	}
	if _, err = os.Lstat(target); !os.IsNotExist(err) {
		t.Fatal("changed-source request created a fixture")
	}
	got, err := os.ReadFile(cached)
	if err != nil || !reflect.DeepEqual(original, got) {
		t.Fatal("existing helper was replaced or deleted")
	}
}

func TestNormalTemplateHelperCanceledOrExpiredLockWaiterReturnsBeforeHolder(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		t.Run(fmt.Sprintf("deadline=%v", deadline), func(t *testing.T) {
			root := t.TempDir()
			held, err := lockFixtureCache(filepath.Join(root, ".normal-helper.lock"))
			if err != nil {
				t.Fatal(err)
			}
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(held) }
			defer release()
			p, count := helperControlPlan(t, root)
			ctx, cancel := context.WithCancel(t.Context())
			if deadline {
				cancel()
				ctx, cancel = context.WithTimeout(t.Context(), 20*time.Millisecond)
			}
			defer cancel()
			returned := make(chan error, 1)
			go func() { _, err := p.cachedHelper(ctx, root); returned <- err }()
			if !deadline {
				cancel()
			}
			select {
			case err := <-returned:
				if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("waiter returned wrong cancellation: %v", err)
				}
			case <-time.After(2 * time.Second):
				release()
				<-returned
				t.Fatal("canceled helper waiter blocked until holder released")
			}
			if count.Load() != 0 {
				t.Fatal("canceled lock waiter compiled a helper")
			}
			artifacts, _ := filepath.Glob(filepath.Join(root, "normal-helper-*"))
			if len(artifacts) != 0 {
				t.Fatal("canceled lock waiter produced artifacts")
			}
		})
	}
}
