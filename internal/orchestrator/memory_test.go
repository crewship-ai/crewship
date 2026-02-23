package orchestrator

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

func TestBuildMemoryInstructions(t *testing.T) {
	today := time.Now().UTC().Format("2006-01-02")
	instructions := buildMemoryInstructions(today)

	if !strings.Contains(instructions, "[MEMORY INSTRUCTIONS]") {
		t.Error("missing [MEMORY INSTRUCTIONS] header")
	}
	if !strings.Contains(instructions, "[END MEMORY INSTRUCTIONS]") {
		t.Error("missing [END MEMORY INSTRUCTIONS] footer")
	}
	if !strings.Contains(instructions, today) {
		t.Errorf("instructions should contain today's date %s", today)
	}
	if !strings.Contains(instructions, "AGENT.md") {
		t.Error("instructions should reference AGENT.md")
	}
	if !strings.Contains(instructions, ".memory/daily/") {
		t.Error("instructions should reference .memory/daily/ path")
	}
}

func TestBuildMemoryInstructionsContent(t *testing.T) {
	instructions := buildMemoryInstructions("2026-02-19")

	expected := []string{
		"persistent memory across sessions",
		"AGENT.md",
		"daily",
		"2026-02-19",
		"remember this",
		"evergreen facts",
	}
	for _, exp := range expected {
		if !strings.Contains(instructions, exp) {
			t.Errorf("instructions missing expected content: %q", exp)
		}
	}
}

// mockContainerForMemory sets up a mock container that returns specific file
// contents for cat commands. Files map keys are full container paths.
func mockContainerForMemory(files map[string]string) *mockContainer {
	// buildMemoryContext calls readContainerFile 3 times (AGENT.md, today, yesterday),
	// then RunAgent calls mkdir (2x), setupClaudeConfig, and the agent exec.
	// We need to map cat calls to file contents.
	mc := &mockContainer{}
	mc.execFn = func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
		// Handle cat commands — return file content or empty for missing
		if len(cfg.Cmd) == 2 && cfg.Cmd[0] == "cat" {
			filePath := cfg.Cmd[1]
			if content, ok := files[filePath]; ok {
				return &provider.ExecResult{
					ExecID: "cat-" + filePath,
					Reader: io.NopCloser(strings.NewReader(content)),
				}, nil
			}
			// File not found — cat writes to stderr, stdout is empty
			return &provider.ExecResult{
				ExecID: "cat-miss",
				Reader: io.NopCloser(strings.NewReader("")),
			}, nil
		}
		// All other commands (mkdir, etc.) — return empty success
		return &provider.ExecResult{
			ExecID: "noop",
			Reader: io.NopCloser(strings.NewReader("")),
		}, nil
	}
	return mc
}

func TestBuildMemoryContext_AllFiles(t *testing.T) {
	today := time.Now().UTC().Format("2006-01-02")
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")

	mc := mockContainerForMemory(map[string]string{
		"/crew/agents/test-agent/.memory/AGENT.md":                    "# Agent\n## Facts\nUser prefers Czech.",
		"/crew/agents/test-agent/.memory/daily/" + today + ".md":      "# Today\nFixed auth bug.",
		"/crew/agents/test-agent/.memory/daily/" + yesterday + ".md":  "# Yesterday\nReviewed PR #42.",
	})

	o := New(mc, newMemState(), slog.Default())

	req := AgentRunRequest{
		AgentSlug:     "test-agent",
		ContainerID:   "c1",
		MemoryEnabled: true,
	}

	ctx := context.Background()
	result := o.buildMemoryContext(ctx, req)

	// Should contain all memory sections
	if !strings.Contains(result, "[AGENT MEMORY]") {
		t.Error("missing [AGENT MEMORY] header")
	}
	if !strings.Contains(result, "[END AGENT MEMORY]") {
		t.Error("missing [END AGENT MEMORY] footer")
	}
	if !strings.Contains(result, "User prefers Czech") {
		t.Error("missing AGENT.md content")
	}
	if !strings.Contains(result, "Fixed auth bug") {
		t.Error("missing today's daily log")
	}
	if !strings.Contains(result, "Reviewed PR #42") {
		t.Error("missing yesterday's daily log")
	}
	if !strings.Contains(result, "[MEMORY INSTRUCTIONS]") {
		t.Error("missing memory instructions")
	}
}

func TestBuildMemoryContext_OnlyAgentMD(t *testing.T) {
	mc := mockContainerForMemory(map[string]string{
		"/crew/agents/test-agent/.memory/AGENT.md": "# Agent\nI am Jarmila.",
	})

	o := New(mc, newMemState(), slog.Default())
	req := AgentRunRequest{
		AgentSlug:     "test-agent",
		ContainerID:   "c1",
		MemoryEnabled: true,
	}

	result := o.buildMemoryContext(context.Background(), req)

	if !strings.Contains(result, "I am Jarmila") {
		t.Error("missing AGENT.md content")
	}
	if !strings.Contains(result, "long-term memory") {
		t.Error("missing 'long-term memory' label")
	}
	if !strings.Contains(result, "[MEMORY INSTRUCTIONS]") {
		t.Error("missing instructions even with partial memory")
	}
}

func TestBuildMemoryContext_NoFiles(t *testing.T) {
	mc := mockContainerForMemory(map[string]string{})

	o := New(mc, newMemState(), slog.Default())
	req := AgentRunRequest{
		AgentSlug:     "test-agent",
		ContainerID:   "c1",
		MemoryEnabled: true,
	}

	result := o.buildMemoryContext(context.Background(), req)

	// Should still return instructions even with no memory files
	if !strings.Contains(result, "[MEMORY INSTRUCTIONS]") {
		t.Error("should return instructions even with no memory files")
	}
	// Should NOT contain the memory section headers (no data to show)
	if strings.Contains(result, "[AGENT MEMORY]") {
		t.Error("should not have [AGENT MEMORY] when no files exist")
	}
}

func TestBuildMemoryContext_Truncation(t *testing.T) {
	// Create AGENT.md that exceeds maxMemoryContextChars
	bigContent := strings.Repeat("This is a long line of memory content. ", 500)

	mc := mockContainerForMemory(map[string]string{
		"/crew/agents/test-agent/.memory/AGENT.md": bigContent,
	})

	o := New(mc, newMemState(), slog.Default())
	req := AgentRunRequest{
		AgentSlug:     "test-agent",
		ContainerID:   "c1",
		MemoryEnabled: true,
	}

	result := o.buildMemoryContext(context.Background(), req)

	// Result should be bounded — AGENT.md alone is >15k chars
	if len(result) > maxMemoryContextChars+2000 { // allow some overhead for headers/instructions
		t.Errorf("result too large: %d chars (max should be ~%d)", len(result), maxMemoryContextChars)
	}
	if !strings.Contains(result, "truncated") {
		t.Error("expected truncation marker")
	}
}

func TestBuildMemoryContext_CatErrorFiltered(t *testing.T) {
	// cat on nonexistent file outputs "cat: /path: No such file or directory"
	mc := mockContainerForMemory(map[string]string{
		"/crew/agents/test-agent/.memory/AGENT.md": "cat: /crew/agents/test-agent/.memory/AGENT.md: No such file or directory",
	})

	o := New(mc, newMemState(), slog.Default())
	req := AgentRunRequest{
		AgentSlug:     "test-agent",
		ContainerID:   "c1",
		MemoryEnabled: true,
	}

	result := o.buildMemoryContext(context.Background(), req)

	// "cat:" prefix should be filtered — treated as no file
	if strings.Contains(result, "No such file") {
		t.Error("cat error message should be filtered out")
	}
}

func TestReadContainerFile_Success(t *testing.T) {
	mc := mockContainerForMemory(map[string]string{
		"/output/agent/.memory/AGENT.md": "Hello World",
	})

	o := New(mc, newMemState(), slog.Default())
	content, err := o.readContainerFile(context.Background(), "c1", "/output/agent/.memory/AGENT.md")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != "Hello World" {
		t.Errorf("expected 'Hello World', got %q", content)
	}
}

func TestReadContainerFile_NotFound(t *testing.T) {
	mc := mockContainerForMemory(map[string]string{})

	o := New(mc, newMemState(), slog.Default())
	content, err := o.readContainerFile(context.Background(), "c1", "/output/agent/.memory/AGENT.md")

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if content != "" {
		t.Errorf("expected empty for missing file, got %q", content)
	}
}

func TestReadContainerFile_ExecError(t *testing.T) {
	mc := &mockContainer{
		execFn: func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
			return nil, fmt.Errorf("container not running")
		},
	}

	o := New(mc, newMemState(), slog.Default())
	content, err := o.readContainerFile(context.Background(), "c1", "/some/path")

	if err == nil {
		t.Error("expected error when exec fails")
	}
	if content != "" {
		t.Errorf("expected empty content on error, got %q", content)
	}
}

func TestReadContainerFile_TrimWhitespace(t *testing.T) {
	mc := mockContainerForMemory(map[string]string{
		"/path": "  content with spaces  \n\n",
	})

	o := New(mc, newMemState(), slog.Default())
	content, _ := o.readContainerFile(context.Background(), "c1", "/path")

	if content != "content with spaces" {
		t.Errorf("expected trimmed content, got %q", content)
	}
}

func TestRunAgentWithMemoryEnabled(t *testing.T) {
	today := time.Now().UTC().Format("2006-01-02")

	// Set up mock that responds to cat and other commands
	agentOutput, agentWriter := io.Pipe()
	go func() {
		agentWriter.Write([]byte("agent response with memory\n"))
		agentWriter.Close()
	}()

	callCount := 0
	mc := &mockContainer{
		execFn: func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
			callCount++
			// Cat calls for memory reading
			if len(cfg.Cmd) == 2 && cfg.Cmd[0] == "cat" {
				if strings.Contains(cfg.Cmd[1], "AGENT.md") {
					return &provider.ExecResult{
						ExecID: "cat-agent",
						Reader: io.NopCloser(strings.NewReader("# Agent Memory\nUser prefers Czech.")),
					}, nil
				}
				if strings.Contains(cfg.Cmd[1], today) {
					return &provider.ExecResult{
						ExecID: "cat-today",
						Reader: io.NopCloser(strings.NewReader("# Today\nWorked on auth.")),
					}, nil
				}
				// Other cat calls (yesterday) — file not found
				return &provider.ExecResult{
					ExecID: "cat-miss",
					Reader: io.NopCloser(strings.NewReader("")),
				}, nil
			}
			// Mkdir calls
			if len(cfg.Cmd) >= 2 && cfg.Cmd[0] == "mkdir" {
				return &provider.ExecResult{
					ExecID: "mkdir",
					Reader: io.NopCloser(strings.NewReader("")),
				}, nil
			}
			// Agent exec (last call) — return agent output
			return &provider.ExecResult{
				ExecID: "agent-exec",
				Reader: agentOutput,
			}, nil
		},
		inspectResult: struct {
			running  bool
			exitCode int
		}{false, 0},
	}

	state := newMemState()
	o := New(mc, state, slog.Default())

	var events []AgentEvent
	handler := func(e AgentEvent) { events = append(events, e) }

	err := o.RunAgent(context.Background(), AgentRunRequest{
		AgentID:       "a1",
		AgentSlug:     "test-agent",
		ChatID:        "s1",
		ContainerID:   "c1",
		CLIAdapter:    "CODEX_CLI",
		UserMessage:   "hello",
		TimeoutSecs:   30,
		MemoryEnabled: true,
	}, handler)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify memory context was injected into system prompt
	// The mock captures events; we can't directly inspect system prompt,
	// but we verify the exec calls happened (cat calls for memory reading)
	if callCount < 5 {
		// Should have: 3 cat calls + 2 mkdir + setupClaudeConfig + agent exec = 7
		t.Errorf("expected at least 5 exec calls (memory reads + dirs + exec), got %d", callCount)
	}
}

func TestRunAgentMemoryDisabledNoExtraCalls(t *testing.T) {
	agentOutput, agentWriter := io.Pipe()
	go func() {
		agentWriter.Write([]byte("response\n"))
		agentWriter.Close()
	}()

	callCount := 0
	mc := &mockContainer{
		execFn: func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
			callCount++
			// Should NOT get cat calls when memory is disabled
			if len(cfg.Cmd) == 2 && cfg.Cmd[0] == "cat" {
				t.Errorf("unexpected cat call with memory disabled: %v", cfg.Cmd)
			}
			if callCount == 3 { // mkdir, setupClaudeConfig, agent exec
				return &provider.ExecResult{ExecID: "exec-1", Reader: agentOutput}, nil
			}
			return &provider.ExecResult{ExecID: "noop", Reader: io.NopCloser(strings.NewReader(""))}, nil
		},
		inspectResult: struct {
			running  bool
			exitCode int
		}{false, 0},
	}

	o := New(mc, newMemState(), slog.Default())

	err := o.RunAgent(context.Background(), AgentRunRequest{
		AgentID:       "a1",
		AgentSlug:     "test-agent",
		ChatID:        "s1",
		ContainerID:   "c1",
		CLIAdapter:    "CODEX_CLI",
		UserMessage:   "hello",
		TimeoutSecs:   30,
		MemoryEnabled: false, // disabled
	}, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunAgentMemoryDirCreation(t *testing.T) {
	agentOutput, agentWriter := io.Pipe()
	go func() {
		agentWriter.Write([]byte("ok\n"))
		agentWriter.Close()
	}()

	var mkdirCmds [][]string
	mc := &mockContainer{
		execFn: func(cfg provider.ExecConfig) (*provider.ExecResult, error) {
			if len(cfg.Cmd) >= 2 && cfg.Cmd[0] == "mkdir" {
				mkdirCmds = append(mkdirCmds, cfg.Cmd)
			}
			if len(cfg.Cmd) >= 2 && cfg.Cmd[0] == "cat" {
				return &provider.ExecResult{ExecID: "cat", Reader: io.NopCloser(strings.NewReader(""))}, nil
			}
			// Return agent output for the last "real" exec
			if len(cfg.Cmd) > 0 && (cfg.Cmd[0] != "mkdir" && cfg.Cmd[0] != "cat" && cfg.Cmd[0] != "sh") {
				return &provider.ExecResult{ExecID: "agent", Reader: agentOutput}, nil
			}
			return &provider.ExecResult{ExecID: "noop", Reader: io.NopCloser(strings.NewReader(""))}, nil
		},
		inspectResult: struct {
			running  bool
			exitCode int
		}{false, 0},
	}

	o := New(mc, newMemState(), slog.Default())

	err := o.RunAgent(context.Background(), AgentRunRequest{
		AgentID:       "a1",
		AgentSlug:     "test-agent",
		ChatID:        "s1",
		ContainerID:   "c1",
		CLIAdapter:    "CODEX_CLI",
		UserMessage:   "hello",
		TimeoutSecs:   30,
		MemoryEnabled: true,
	}, nil)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have 2 mkdir calls: one for scratch+output, one for memory dirs
	if len(mkdirCmds) < 2 {
		t.Fatalf("expected at least 2 mkdir calls, got %d", len(mkdirCmds))
	}

	// Second mkdir should create .memory/ dirs
	memoryMkdir := mkdirCmds[1]
	joined := strings.Join(memoryMkdir, " ")
	if !strings.Contains(joined, ".memory") {
		t.Errorf("expected .memory in second mkdir, got: %v", memoryMkdir)
	}
	if !strings.Contains(joined, ".snapshots") {
		t.Errorf("expected .snapshots in memory mkdir, got: %v", memoryMkdir)
	}
	if !strings.Contains(joined, "daily") {
		t.Errorf("expected daily in memory mkdir, got: %v", memoryMkdir)
	}
}
