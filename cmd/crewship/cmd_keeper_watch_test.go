package main

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

func TestKeeperWatchCmdStructure(t *testing.T) {
	t.Parallel()

	have := map[string]bool{}
	for _, sub := range keeperWatchCmd.Commands() {
		have[sub.Name()] = true
	}
	for _, want := range []string{"get", "set", "clear", "preset"} {
		if !have[want] {
			t.Errorf("keeper watch missing subcommand %q; have %v", want, have)
		}
	}
	presetSubs := map[string]bool{}
	for _, sub := range keeperWatchPresetCmd.Commands() {
		presetSubs[sub.Name()] = true
	}
	for _, want := range []string{"list", "add", "remove"} {
		if !presetSubs[want] {
			t.Errorf("keeper watch preset missing subcommand %q; have %v", want, presetSubs)
		}
	}
}

func TestKeeperWatchSet_SendsWatchSpecOnly(t *testing.T) {
	m := startKeeperMock(t)

	if err := keeperWatchSetCmd.RunE(keeperWatchSetCmd, []string{"flag any read of ~/.ssh"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	body := m.decodePut(t)
	if body["watch_spec"] != "flag any read of ~/.ssh" {
		t.Errorf("watch_spec = %v", body["watch_spec"])
	}
	// set is a single-field partial update — it must not read-merge or carry
	// unrelated keys.
	for _, other := range []string{"enabled", "watch_presets", "deny_notify_min_risk"} {
		if _, ok := body[other]; ok {
			t.Errorf("set must not carry %q; body=%v", other, body)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.govGets != 0 {
		t.Errorf("set must not GET governance (no read-merge); got %d", m.govGets)
	}
}

func TestKeeperWatchSet_RejectsOverlong(t *testing.T) {
	m := startKeeperMock(t)
	huge := strings.Repeat("x", 5000) // > MaxWatchSpecLen (4096)

	err := keeperWatchSetCmd.RunE(keeperWatchSetCmd, []string{huge})
	if err == nil || !strings.Contains(err.Error(), "maximum") {
		t.Errorf("expected length error; got %v", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.putBody) != 0 {
		t.Errorf("over-long spec must not PUT; got %s", m.putBody)
	}
}

func TestKeeperWatchSet_Stdin(t *testing.T) {
	m := startKeeperMock(t)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = orig })

	go func() {
		_, _ = w.WriteString("rule one\nrule two\n")
		_ = w.Close()
	}()

	if err := keeperWatchSetCmd.RunE(keeperWatchSetCmd, []string{"-"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	body := m.decodePut(t)
	if body["watch_spec"] != "rule one\nrule two" {
		t.Errorf("stdin watch_spec = %q", body["watch_spec"])
	}
}

func TestKeeperWatchClear_SendsEmptySpec(t *testing.T) {
	m := startKeeperMock(t)

	if err := keeperWatchClearCmd.RunE(keeperWatchClearCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	body := m.decodePut(t)
	if v, ok := body["watch_spec"]; !ok || v != "" {
		t.Errorf("clear must send watch_spec=\"\"; got %v (present=%v)", v, ok)
	}
}

func TestKeeperWatchPresetAdd_ReadMerges(t *testing.T) {
	m := startKeeperMock(t) // default presets: ["credentials"]

	if err := keeperWatchPresetAddCmd.RunE(keeperWatchPresetAddCmd, []string{"egress"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	body := m.decodePut(t)
	got := toStringSlice(body["watch_presets"])
	want := []string{"credentials", "egress"} // sorted, merged with the existing set
	if !reflect.DeepEqual(got, want) {
		t.Errorf("watch_presets = %v, want %v", got, want)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.govGets != 1 {
		t.Errorf("preset add must GET once to read-merge; got %d", m.govGets)
	}
}

func TestKeeperWatchPresetRemove_ReadMerges(t *testing.T) {
	m := startKeeperMock(t)
	m.mu.Lock()
	m.gov["watch_presets"] = []any{"credentials", "egress"}
	m.mu.Unlock()

	if err := keeperWatchPresetRemoveCmd.RunE(keeperWatchPresetRemoveCmd, []string{"egress"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	body := m.decodePut(t)
	got := toStringSlice(body["watch_presets"])
	want := []string{"credentials"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("watch_presets = %v, want %v", got, want)
	}
}

func TestKeeperWatchPresetAdd_UnknownKeyNoPut(t *testing.T) {
	m := startKeeperMock(t)

	err := keeperWatchPresetAddCmd.RunE(keeperWatchPresetAddCmd, []string{"bogus"})
	if err == nil || !strings.Contains(err.Error(), "bogus") {
		t.Errorf("expected unknown-preset error; got %v", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.putBody) != 0 {
		t.Errorf("unknown preset must not PUT; got %s", m.putBody)
	}
	if m.govGets != 0 {
		t.Errorf("unknown preset must fail before any GET; got %d", m.govGets)
	}
}

func TestKeeperWatchPresetList_Renders(t *testing.T) {
	m := startKeeperMock(t)

	if err := keeperWatchPresetListCmd.RunE(keeperWatchPresetListCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.govGets != 1 {
		t.Errorf("preset list should GET once; got %d", m.govGets)
	}
}

func TestKeeperWatchGet_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}
	if err := keeperWatchGetCmd.RunE(keeperWatchGetCmd, nil); err == nil {
		t.Error("expected auth error")
	}
}

func toStringSlice(v any) []string {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, e := range raw {
		out = append(out, e.(string))
	}
	return out
}
