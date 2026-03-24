package orchestrator

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

const (
	defaultMemoryContextChars = 15000
	memoryReadTimeout         = 5 * time.Second
)

// buildMemoryContext reads agent memory files from the container and returns
// a formatted block for system prompt injection. Caller should gate on
// req.MemoryEnabled. charBudget controls the maximum character size; pass 0
// to use the default (15000 chars). When no memory files exist, returns only
// the memory instructions block.
func (o *Orchestrator) buildMemoryContext(ctx context.Context, req AgentRunRequest, charBudget int) string {
	if charBudget <= 0 {
		charBudget = defaultMemoryContextChars
	}
	memoryDir := path.Join("/crew", "agents", req.AgentSlug, ".memory")
	agentMDPath := path.Join(memoryDir, "AGENT.md")

	readCtx, cancel := context.WithTimeout(ctx, memoryReadTimeout)
	defer cancel()

	// Read AGENT.md (primary long-term memory)
	agentMD, _ := o.readContainerFile(readCtx, req.ContainerID, agentMDPath)

	// Read today's daily log
	today := time.Now().UTC().Format("2006-01-02")
	todayPath := path.Join(memoryDir, "daily", today+".md")
	todayLog, _ := o.readContainerFile(readCtx, req.ContainerID, todayPath)

	// Read yesterday's daily log for continuity
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	yesterdayPath := path.Join(memoryDir, "daily", yesterday+".md")
	yesterdayLog, _ := o.readContainerFile(readCtx, req.ContainerID, yesterdayPath)

	// If no memory files exist, return just the instructions
	if agentMD == "" && todayLog == "" && yesterdayLog == "" {
		return buildMemoryInstructions(today)
	}

	var b strings.Builder
	totalChars := 0

	b.WriteString("[AGENT MEMORY]\n")

	if agentMD != "" {
		section := fmt.Sprintf("--- AGENT.md (long-term memory) ---\n%s\n", agentMD)
		if totalChars+len(section) > charBudget {
			section = section[:charBudget-totalChars] + "\n...(truncated)"
		}
		b.WriteString(section)
		totalChars += len(section)
	}

	if yesterdayLog != "" && totalChars < charBudget {
		section := fmt.Sprintf("\n--- Daily log: %s (yesterday) ---\n%s\n", yesterday, yesterdayLog)
		if totalChars+len(section) > charBudget {
			remaining := charBudget - totalChars
			if remaining > 100 {
				section = section[:remaining] + "\n...(truncated)"
			} else {
				section = ""
			}
		}
		if section != "" {
			b.WriteString(section)
			totalChars += len(section)
		}
	}

	if todayLog != "" && totalChars < charBudget {
		section := fmt.Sprintf("\n--- Daily log: %s (today) ---\n%s\n", today, todayLog)
		if totalChars+len(section) > charBudget {
			remaining := charBudget - totalChars
			if remaining > 100 {
				section = section[:remaining] + "\n...(truncated)"
			} else {
				section = ""
			}
		}
		if section != "" {
			b.WriteString(section)
		}
	}

	b.WriteString("[END AGENT MEMORY]\n\n")
	b.WriteString(buildMemoryInstructions(today))

	return b.String()
}

// buildMemoryInstructions returns the instruction block that teaches the agent
// how to use persistent memory.
func buildMemoryInstructions(today string) string {
	return fmt.Sprintf(`[MEMORY INSTRUCTIONS]
You have persistent memory across sessions. Your long-term memory and recent daily
logs are shown above (if any exist).

WRITING MEMORY:
- Write lasting facts, preferences, and project context to: ~/.memory/AGENT.md
- Write daily session notes and decisions to: ~/.memory/daily/%s.md
- Use today's date (%s) for the daily log filename.
- Write early and often -- do not wait until the end of the session.

GUIDELINES:
- AGENT.md is for curated, evergreen facts (identity, learned facts, preferences).
- Daily logs are for session-specific notes (what you did, decisions made, observations).
- If the user says "remember this", write it to AGENT.md immediately.
- Before starting complex tasks, check your memory for relevant past context.
- When updating AGENT.md, ADD new information. Do not delete existing entries unless outdated.
[END MEMORY INSTRUCTIONS]`, today, today)
}

// readContainerFile reads a file from the container via Exec("cat", path).
// Returns the file content as a string, or empty string + error if the file
// doesn't exist or can't be read.
func (o *Orchestrator) readContainerFile(ctx context.Context, containerID, filePath string) (string, error) {
	cfg := provider.ExecConfig{
		ContainerID: containerID,
		Cmd:         []string{"cat", filePath},
		User:        "1001:1001",
	}

	result, err := o.container.Exec(ctx, cfg)
	if err != nil {
		return "", fmt.Errorf("exec cat %s: %w", filePath, err)
	}
	defer result.Reader.Close()

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, result.Reader); err != nil {
		return "", fmt.Errorf("read %s: %w", filePath, err)
	}

	content := strings.TrimSpace(buf.String())

	// "cat" on a non-existent file writes to stderr, stdout is empty.
	// Also filter out common shell error messages.
	if content == "" || strings.HasPrefix(content, "cat:") {
		return "", nil
	}

	return content, nil
}
