package cli

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeFakeTool drops an executable shell script named `name` into dir.
// The script appends each argv element on its own line to recordPath and
// exits with the given code. Tests point PATH at dir so exec.LookPath /
// exec.Command resolve the fake instead of any real system tool.
func writeFakeTool(t *testing.T, dir, name, recordPath string, exitCode int) {
	t.Helper()
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do printf '%s\\n' \"$a\" >> \"" + recordPath + "\"; done\n" +
		"exit " + map[int]string{0: "0", 1: "1"}[exitCode] + "\n"
	if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func readRecord(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read record: %v", err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

func TestOSNotifyLinux_UrgencyMapping(t *testing.T) {
	tests := []struct {
		level       NotifyLevel
		wantUrgency string
	}{
		{NotifyInfo, "--urgency=low"},
		{NotifyWarn, "--urgency=normal"},
		{NotifyCritical, "--urgency=critical"},
	}
	for _, tt := range tests {
		dir := t.TempDir()
		record := filepath.Join(dir, "record.txt")
		writeFakeTool(t, dir, "notify-send", record, 0)
		t.Setenv("PATH", dir)

		if err := osNotifyLinux(context.Background(), "Title", "Body", tt.level); err != nil {
			t.Fatalf("osNotifyLinux(%v): %v", tt.level, err)
		}
		args := readRecord(t, record)
		want := []string{tt.wantUrgency, "--app-name=Crewship", "Title", "Body"}
		if len(args) != len(want) {
			t.Fatalf("level %v: args = %v, want %v", tt.level, args, want)
		}
		for i := range want {
			if args[i] != want[i] {
				t.Errorf("level %v: arg[%d] = %q, want %q", tt.level, i, args[i], want[i])
			}
		}
	}
}

func TestOSNotifyLinux_MissingTool(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // empty dir: no notify-send anywhere
	err := osNotifyLinux(context.Background(), "T", "B", NotifyInfo)
	if err == nil || !strings.Contains(err.Error(), "notify-send not installed") {
		t.Errorf("err = %v, want notify-send not installed", err)
	}
}

func TestOSNotifyLinux_ExecFailure(t *testing.T) {
	dir := t.TempDir()
	writeFakeTool(t, dir, "notify-send", filepath.Join(dir, "r.txt"), 1)
	t.Setenv("PATH", dir)
	err := osNotifyLinux(context.Background(), "T", "B", NotifyWarn)
	if err == nil || !strings.Contains(err.Error(), "notify-send:") {
		t.Errorf("err = %v, want wrapped notify-send error", err)
	}
}

func TestOSNotifyWindows_MissingPowershell(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	err := osNotifyWindows(context.Background(), "T", "B")
	if err == nil || !strings.Contains(err.Error(), "powershell not found") {
		t.Errorf("err = %v, want powershell not found", err)
	}
}

func TestOSNotifyWindows_EscapesMetacharacters(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "record.txt")
	writeFakeTool(t, dir, "powershell.exe", record, 0)
	t.Setenv("PATH", dir)

	title := `Ti"tle $env:HOME ` + "`tick"
	body := "line1\r\nline2"
	if err := osNotifyWindows(context.Background(), title, body); err != nil {
		t.Fatalf("osNotifyWindows: %v", err)
	}
	args := readRecord(t, record)
	if len(args) < 3 || args[0] != "-NoProfile" || args[1] != "-Command" {
		t.Fatalf("argv = %v, want -NoProfile -Command <script>", args)
	}
	ps := strings.Join(args[2:], "\n")
	if !strings.Contains(ps, "`\"") {
		t.Errorf("double quote not escaped in: %s", ps)
	}
	if !strings.Contains(ps, "`$env:HOME") {
		t.Errorf("dollar not escaped in: %s", ps)
	}
	if !strings.Contains(ps, "``tick") {
		t.Errorf("backtick not escaped in: %s", ps)
	}
	if !strings.Contains(ps, "line1 line2") {
		t.Errorf("newline not collapsed to space in: %s", ps)
	}
	if !strings.Contains(ps, "BurntToast") {
		t.Errorf("script should reference BurntToast: %s", ps)
	}
}

func TestOSNotifyWindows_ExecFailure(t *testing.T) {
	dir := t.TempDir()
	writeFakeTool(t, dir, "powershell.exe", filepath.Join(dir, "r.txt"), 1)
	t.Setenv("PATH", dir)
	err := osNotifyWindows(context.Background(), "T", "B")
	if err == nil || !strings.Contains(err.Error(), "powershell:") {
		t.Errorf("err = %v, want wrapped powershell error", err)
	}
}

func TestOSNotifyDarwin_EscapesQuotesAndNewlines(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(dir, "record.txt")
	writeFakeTool(t, dir, "osascript", record, 0)
	t.Setenv("PATH", dir)

	if err := osNotifyDarwin(context.Background(), `He said "hi"`, "back\\slash\nnext"); err != nil {
		t.Fatalf("osNotifyDarwin: %v", err)
	}
	args := readRecord(t, record)
	if len(args) < 2 || args[0] != "-e" {
		t.Fatalf("argv = %v, want -e <script>", args)
	}
	script := strings.Join(args[1:], "\n")
	if !strings.Contains(script, `\"hi\"`) {
		t.Errorf("double quotes not escaped: %s", script)
	}
	if !strings.Contains(script, `back\\slash next`) {
		t.Errorf("backslash not doubled / newline not collapsed: %s", script)
	}
	if !strings.Contains(script, "display notification") {
		t.Errorf("script missing display notification: %s", script)
	}
}

func TestOSNotifyDarwin_ExecFailure(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\necho boom-output\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "osascript"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	err := osNotifyDarwin(context.Background(), "T", "B")
	if err == nil || !strings.Contains(err.Error(), "osascript:") {
		t.Fatalf("err = %v, want wrapped osascript error", err)
	}
	if !strings.Contains(err.Error(), "boom-output") {
		t.Errorf("err = %v, want combined output included", err)
	}
}

// TestOSNotify_DefaultTitleAndNilCtx exercises the OSNotify dispatcher on
// the host platform (darwin in CI/dev here): empty title falls back to
// "Crewship" and a nil ctx is tolerated. The platform tool is faked via
// PATH so no real notification is shown.
func TestOSNotify_DefaultTitleAndNilCtx(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skipf("dispatcher test wired for darwin host, GOOS=%s", runtime.GOOS)
	}
	dir := t.TempDir()
	record := filepath.Join(dir, "record.txt")
	writeFakeTool(t, dir, "osascript", record, 0)
	t.Setenv("PATH", dir)

	if err := OSNotify(nil, "", "hello", NotifyInfo); err != nil { //nolint:staticcheck // nil ctx is the documented fallback path
		t.Fatalf("OSNotify: %v", err)
	}
	script := strings.Join(readRecord(t, record), "\n")
	if !strings.Contains(script, `with title "Crewship"`) {
		t.Errorf("empty title should default to Crewship, got: %s", script)
	}
	if !strings.Contains(script, "hello") {
		t.Errorf("body missing from script: %s", script)
	}
}
