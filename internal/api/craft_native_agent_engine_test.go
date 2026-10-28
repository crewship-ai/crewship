package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func nativeAgentRejectProbe(t *testing.T) (CraftNativeDocumentProfile, string) {
	t.Helper()
	p := craftNativeProfileFixture(t)
	marker := filepath.Join(t.TempDir(), "executed")
	quoted := "'" + strings.ReplaceAll(marker, "'", "'\\''") + "'"
	raw := []byte("#!/bin/sh\nprintf x >> " + quoted + "\nexit 99\n")
	if err := os.WriteFile(p.CLIPath, raw, 0700); err != nil {
		t.Fatal(err)
	}
	p.CLISHA256 = fmt.Sprintf("%x", sha256.Sum256(raw))
	return p, marker
}

func nativeAgentNotExecuted(t *testing.T, marker string) {
	t.Helper()
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("rejected input reached the executable")
	}
}

func TestCraftNativeAgentCLIRejectsUnsafeCommandsBeforeExecution(t *testing.T) {
	a := NewCraftNativeAgentCLIAdapter()
	p, marker := nativeAgentRejectProbe(t)
	for _, raw := range []string{
		`null`, `{}`, `[]`, `[null]`, `["text.insert"]`,
		`[{"command":"file.open","params":{"path":"/etc/passwd"}}]`,
		`[{"command":"file.save","params":{"path":"/tmp/escape"}}]`,
		`[{"command":"text.insert","params":{"text":"x","path":"/tmp/escape"}}]`,
		`[{"command":"text.insert","params":{"text":true}}]`,
		`[{"command":"text.insert","params":{"text":""}}]`,
		`[{"command":"caret.docStart","params":{}}]`,
		`[{"command":"text.insert","params":{"text":"x"},"file":"/etc/passwd"}]`,
		`[{"command":"text.insert","command":"file.open","params":{"text":"x"}}]`,
		`[{"command":"cell.set","params":{"cell":"A1","input":"x"}}]`,
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := a.Apply(context.Background(), p, []byte(`{"body":[]}`), json.RawMessage(raw)); err == nil {
				t.Fatal("unsafe or malformed command was accepted")
			}
			nativeAgentNotExecuted(t, marker)
		})
	}
	p.Application = "gridcraft"
	for _, cell := range []string{"../file", "A0", "XFE1", "A1048577", "A1:B2", "Sheet1!A1"} {
		q, _ := json.Marshal([]any{map[string]any{"command": "cell.set", "params": map[string]any{"cell": cell, "input": "x"}}})
		if _, err := a.Apply(context.Background(), p, []byte(`{"sheets":[]}`), q); err == nil {
			t.Fatalf("invalid cell address %q accepted", cell)
		}
		nativeAgentNotExecuted(t, marker)
	}
	for _, input := range []json.RawMessage{json.RawMessage(`null`), json.RawMessage(`true`), json.RawMessage(`12`), json.RawMessage(`"=WEBSERVICE(\"file:///etc/passwd\")"`), json.RawMessage(`"+WEBSERVICE(\"https://example.invalid\")"`), json.RawMessage(`"-IMPORTDATA(\"/etc/passwd\")"`), json.RawMessage(`" =A1+1"`), json.RawMessage(`"=1+1"`)} {
		q := json.RawMessage(`[{"command":"cell.set","params":{"cell":"A1","input":` + string(input) + `}}]`)
		if _, err := a.Apply(context.Background(), p, []byte(`{"sheets":[]}`), q); err == nil {
			t.Fatalf("unsafe formula or non-string input accepted: %s", input)
		}
		nativeAgentNotExecuted(t, marker)
	}
}

func TestCraftNativeAgentCLIRejectsPinMismatch(t *testing.T) {
	p, marker := nativeAgentRejectProbe(t)
	p.CLISHA256 = strings.Repeat("0", 64)
	a := NewCraftNativeAgentCLIAdapter()
	if _, err := a.Apply(context.Background(), p, []byte(`{"body":[]}`), json.RawMessage(`[{"command":"text.insert","params":{"text":"x"}}]`)); err == nil {
		t.Fatal("engine pin mismatch accepted")
	}
	if _, err := a.Inspect(context.Background(), p, []byte(`{"body":[]}`), json.RawMessage(`{"command":"text","params":{}}`)); err == nil {
		t.Fatal("inspection pin mismatch accepted")
	}
	nativeAgentNotExecuted(t, marker)
}

func TestCraftNativeAgentCLIQueryAndBudgetAllowlist(t *testing.T) {
	p, marker := nativeAgentRejectProbe(t)
	a := NewCraftNativeAgentCLIAdapter()
	for _, q := range []string{`null`, `[]`, `{"command":"file.info","params":{}}`, `{"command":"text","params":{"file":"/etc/passwd"}}`, `{"command":"text","params":{},"path":"x"}`} {
		if _, err := a.Inspect(context.Background(), p, []byte(`{"body":[]}`), json.RawMessage(q)); err == nil {
			t.Fatal("unsafe query accepted")
		}
		nativeAgentNotExecuted(t, marker)
	}
	for _, q := range []json.RawMessage{
		json.RawMessage(`[{"command":"text.insert","params":{"text":"` + strings.Repeat("x", (64<<10)+1) + `"}}]`),
		json.RawMessage("[" + strings.TrimSuffix(strings.Repeat(`{"command":"text.insert","params":{"text":"x"}},`, 65), ",") + "]"),
	} {
		if _, err := a.Apply(context.Background(), p, []byte(`{"body":[]}`), q); err == nil {
			t.Fatal("unbounded operation accepted")
		}
		nativeAgentNotExecuted(t, marker)
	}
	for _, scalar := range []string{"", "41", "-4.5", "+2", "TRUE", "Plain text", "'=WEBSERVICE(\"url\")"} {
		q, _ := json.Marshal([]any{map[string]any{"command": "cell.set", "params": map[string]string{"cell": "XFD1048576", "input": scalar}}})
		if _, err := nativeAgentEngineCommands("gridcraft", q, false); err != nil {
			t.Fatalf("bounded scalar rejected: %q", scalar)
		}
	}
}

func TestCraftNativeAgentCLILinuxOnly(t *testing.T) {
	if runtime.GOOS == "linux" {
		return
	}
	if _, err := nativeAgentEngineExecutable(craftNativeProfileFixture(t)); err == nil {
		t.Fatal("unsupported launcher accepted")
	}
}
