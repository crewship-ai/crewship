package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/mooring"
)

var ErrCraftNativeAgentEngine = errors.New("native Craft agent engine operation refused")

// CraftNativeAgentCLIAdapter accepts a small command language, not an engine
// script. Executables and document paths come exclusively from trusted code.
type CraftNativeAgentCLIAdapter struct{ timeout time.Duration }

func NewCraftNativeAgentCLIAdapter() *CraftNativeAgentCLIAdapter {
	return &CraftNativeAgentCLIAdapter{timeout: 30 * time.Second}
}

type nativeAgentEngineCommand struct {
	Command string
	Params  json.RawMessage
}

func nativeAgentEngineObject(raw []byte, keys ...string) (map[string]json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil || len(fields) != len(keys) {
		return nil, false
	}
	for _, key := range keys {
		if fields[key] == nil {
			return nil, false
		}
	}
	return fields, true
}

func nativeAgentEngineCell(raw json.RawMessage) bool {
	var value string
	if json.Unmarshal(raw, &value) != nil || len(value) < 2 || len(value) > 10 {
		return false
	}
	col, i := 0, 0
	for i < len(value) && value[i] >= 'A' && value[i] <= 'Z' {
		if i >= 3 {
			return false
		}
		col = col*26 + int(value[i]-'A'+1)
		i++
	}
	if col < 1 || col > 16384 || i == len(value) || value[i] < '1' || value[i] > '9' {
		return false
	}
	for _, ch := range value[i:] {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	row, err := strconv.Atoi(value[i:])
	return err == nil && row >= 1 && row <= 1048576
}

// Only scalar/literal cell entry is enabled. In particular Excel-style leading
// +FUNC/-FUNC also enters formula parsing in the qualified original engine.
func nativeAgentEngineScalar(value string) bool {
	text := strings.TrimSpace(value)
	if strings.HasPrefix(text, "=") {
		return false
	}
	if strings.HasPrefix(text, "+") || strings.HasPrefix(text, "-") {
		number, err := strconv.ParseFloat(text, 64)
		return err == nil && !math.IsNaN(number) && !math.IsInf(number, 0)
	}
	return true
}

func nativeAgentEngineCommands(application string, raw json.RawMessage, inspect bool) ([]nativeAgentEngineCommand, error) {
	if len(raw) == 0 || len(raw) > 256<<10 || !utf8.Valid(raw) {
		return nil, ErrCraftNativeAgentEngine
	}
	canonical, err := mooring.CanonicalJSON(raw)
	if err != nil {
		return nil, ErrCraftNativeAgentEngine
	}
	var entries []json.RawMessage
	if inspect {
		entries = []json.RawMessage{canonical}
	} else if json.Unmarshal(canonical, &entries) != nil || len(entries) == 0 || len(entries) > 64 {
		return nil, ErrCraftNativeAgentEngine
	}
	commands := make([]nativeAgentEngineCommand, 0, len(entries))
	mutates := false
	for _, entry := range entries {
		fields, ok := nativeAgentEngineObject(entry, "command", "params")
		var name string
		if !ok || json.Unmarshal(fields["command"], &name) != nil {
			return nil, ErrCraftNativeAgentEngine
		}
		params := fields["params"]
		switch {
		case application == "wordcraft" && inspect && name == "text":
			_, ok = nativeAgentEngineObject(params)
		case application == "wordcraft" && !inspect && (name == "caret.docStart" || name == "caret.docEnd"):
			_, ok = nativeAgentEngineObject(params)
		case application == "wordcraft" && !inspect && name == "text.insert":
			var text string
			var p map[string]json.RawMessage
			p, ok = nativeAgentEngineObject(params, "text")
			ok = ok && json.Unmarshal(p["text"], &text) == nil && len(text) > 0 && len(text) <= 64<<10 && utf8.ValidString(text)
			mutates = true
		case application == "gridcraft" && !inspect && name == "cell.set":
			var input string
			var p map[string]json.RawMessage
			p, ok = nativeAgentEngineObject(params, "cell", "input")
			ok = ok && nativeAgentEngineCell(p["cell"]) && len(p["input"]) > 0 && p["input"][0] == '"' && json.Unmarshal(p["input"], &input) == nil && len(input) <= 32<<10 && utf8.ValidString(input) && nativeAgentEngineScalar(input)
			mutates = true
		case application == "gridcraft" && inspect && name == "cell.get":
			var p map[string]json.RawMessage
			p, ok = nativeAgentEngineObject(params, "cell")
			ok = ok && nativeAgentEngineCell(p["cell"])
		default:
			ok = false
		}
		if !ok {
			return nil, ErrCraftNativeAgentEngine
		}
		commands = append(commands, nativeAgentEngineCommand{name, params})
	}
	if !inspect && !mutates {
		return nil, ErrCraftNativeAgentEngine
	}
	return commands, nil
}

type nativeAgentEngineOutput struct {
	bytes.Buffer
	limit int
}

func (b *nativeAgentEngineOutput) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, ErrCraftNativeAgentEngine
	}
	return b.Buffer.Write(p)
}

func nativeAgentEngineExecutable(p CraftNativeDocumentProfile) (*os.File, error) {
	if runtime.GOOS != "linux" || (p.Application != "wordcraft" && p.Application != "gridcraft") || !filepath.IsAbs(p.CLIPath) || filepath.Clean(p.CLIPath) != p.CLIPath || !craftNativePin(p.CLISHA256, 64) {
		return nil, ErrCraftNativeAgentEngine
	}
	f, err := os.Open(p.CLIPath)
	if err != nil {
		return nil, ErrCraftNativeAgentEngine
	}
	fail := func() (*os.File, error) { f.Close(); return nil, ErrCraftNativeAgentEngine }
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 || info.Size() <= 0 || info.Size() > 512<<20 {
		return fail()
	}
	sum := sha256.New()
	if _, err = io.Copy(sum, io.LimitReader(f, (512<<20)+1)); err != nil || hex.EncodeToString(sum.Sum(nil)) != p.CLISHA256 {
		return fail()
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return fail()
	}
	return f, nil
}

func (a *CraftNativeAgentCLIAdapter) run(ctx context.Context, p CraftNativeDocumentProfile, raw []byte, commands []nativeAgentEngineCommand, inspect bool) ([]byte, error) {
	if len(raw) == 0 || len(raw) > 32<<20 || !utf8.Valid(raw) || len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' {
		return nil, ErrCraftNativeAgentEngine
	}
	if !json.Valid(raw) || a == nil || a.timeout <= 0 || a.timeout > time.Minute {
		return nil, ErrCraftNativeAgentEngine
	}
	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	// Refuse import that silently drops authored fields before editing it. Store
	// validation remains independent and validates the returned candidate again.
	v := mooring.NativeCLIValidator{Application: p.Application, BinaryPath: p.CLIPath, SHA256: p.CLISHA256, Timeout: a.timeout}
	if err := v.Validate(ctx, raw); err != nil {
		return nil, ErrCraftNativeAgentEngine
	}
	f, err := nativeAgentEngineExecutable(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	dir, err := os.MkdirTemp("", "craft-native-agent-")
	if err != nil {
		return nil, ErrCraftNativeAgentEngine
	}
	defer os.RemoveAll(dir)
	input, output := filepath.Join(dir, "document.json"), filepath.Join(dir, "result.json")
	if err = os.WriteFile(input, raw, 0600); err != nil {
		return nil, ErrCraftNativeAgentEngine
	}
	var args []string
	if p.Application == "wordcraft" {
		args = []string{"run", "--file", input}
		if inspect {
			args = []string{"text", input}
		}
	} else {
		args = []string{"run", "--in", input}
		if !inspect {
			args = append(args, "--quiet")
		}
	}
	if !inspect || p.Application == "gridcraft" {
		for _, command := range commands {
			args = append(args, "--cmd", command.Command+"="+string(command.Params))
		}
	}
	if !inspect {
		flag := "--save"
		if p.Application == "gridcraft" {
			flag = "--out"
		}
		args = append(args, flag, output)
	}
	// Execute the descriptor we hashed, never look up the path again at exec.
	cmd := exec.CommandContext(ctx, "/proc/self/fd/3", args...)
	cmd.ExtraFiles, cmd.Dir = []*os.File{f}, dir
	cmd.Env = []string{"LANG=C.UTF-8", "HOME=" + dir, "TMPDIR=" + dir}
	cmd.WaitDelay = 100 * time.Millisecond
	stdout := &nativeAgentEngineOutput{limit: 1 << 20}
	stderr := &nativeAgentEngineOutput{limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err = cmd.Run(); err != nil {
		return nil, ErrCraftNativeAgentEngine
	}
	if inspect {
		if stderr.Len() != 0 || !utf8.Valid(stdout.Bytes()) {
			return nil, ErrCraftNativeAgentEngine
		}
		if p.Application == "wordcraft" {
			return json.Marshal(map[string]string{"text": strings.TrimSuffix(stdout.String(), "\n")})
		}
		result := bytes.TrimSpace(stdout.Bytes())
		fields, ok := nativeAgentEngineObject(result, "command", "result")
		if !json.Valid(result) || !ok || string(fields["command"]) != `"cell.get"` {
			return nil, ErrCraftNativeAgentEngine
		}
		return append([]byte(nil), result...), nil
	}
	info, err := os.Lstat(output)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 32<<20 {
		return nil, ErrCraftNativeAgentEngine
	}
	expected := "wrote " + output + "\n"
	if p.Application == "gridcraft" {
		expected = "wrote " + output + " (" + strconv.FormatInt(info.Size(), 10) + " bytes)\n"
	}
	if stdout.Len() != 0 || stderr.String() != expected {
		return nil, ErrCraftNativeAgentEngine
	}
	result, err := os.ReadFile(output)
	if err != nil || len(result) > 32<<20 || !utf8.Valid(result) || len(bytes.TrimSpace(result)) == 0 || bytes.TrimSpace(result)[0] != '{' {
		return nil, ErrCraftNativeAgentEngine
	}
	if !json.Valid(result) {
		return nil, ErrCraftNativeAgentEngine
	}
	return result, nil
}

func (a *CraftNativeAgentCLIAdapter) Apply(ctx context.Context, p CraftNativeDocumentProfile, raw []byte, commands json.RawMessage) ([]byte, error) {
	parsed, err := nativeAgentEngineCommands(p.Application, commands, false)
	if err != nil {
		return nil, err
	}
	return a.run(ctx, p, raw, parsed, false)
}

func (a *CraftNativeAgentCLIAdapter) Inspect(ctx context.Context, p CraftNativeDocumentProfile, raw []byte, query json.RawMessage) (json.RawMessage, error) {
	parsed, err := nativeAgentEngineCommands(p.Application, query, true)
	if err != nil {
		return nil, err
	}
	return a.run(ctx, p, raw, parsed, true)
}
