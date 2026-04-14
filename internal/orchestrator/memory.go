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
	crewMemoryMaxPct          = 40 // crew memory capped at 40% of total budget
)

// buildMemoryContext reads agent and crew memory files from the container and
// returns a formatted block for system prompt injection. Caller should gate on
// req.MemoryEnabled. charBudget controls the maximum character size; pass 0
// to use the default (15000 chars).
func (o *Orchestrator) buildMemoryContext(ctx context.Context, req AgentRunRequest, charBudget int) string {
	if charBudget <= 0 {
		charBudget = defaultMemoryContextChars
	}
	today := time.Now().UTC().Format("2006-01-02")

	// --- Crew memory first (measure actual size, cap at 40%) ---
	var crewBlock string
	var crewUsed int
	if req.CrewID != "" {
		crewBudget := charBudget * crewMemoryMaxPct / 100
		crewBlock, crewUsed = o.buildCrewMemoryBlock(ctx, req, crewBudget, today)
	}

	// --- Agent memory gets remainder (dynamic reclaim from empty crew) ---
	agentBudget := charBudget - crewUsed
	agentBlock := o.buildAgentMemoryBlock(ctx, req, agentBudget, today)

	// If no memory files at all, return just instructions
	if agentBlock == "" && crewBlock == "" {
		return buildMemoryInstructions(today)
	}

	var b strings.Builder
	if agentBlock != "" {
		b.WriteString(agentBlock)
	}
	if crewBlock != "" {
		b.WriteString(crewBlock)
	}
	b.WriteString(buildMemoryInstructions(today))

	return b.String()
}

// buildAgentMemoryBlock reads per-agent memory files and returns a formatted
// block with the [AGENT MEMORY] markers. Returns empty string if no files exist.
func (o *Orchestrator) buildAgentMemoryBlock(ctx context.Context, req AgentRunRequest, budget int, today string) string {
	memoryDir := path.Join("/crew", "agents", req.AgentSlug, ".memory")
	agentMDPath := path.Join(memoryDir, "AGENT.md")

	readCtx, cancel := context.WithTimeout(ctx, memoryReadTimeout)
	defer cancel()

	agentMD, _ := o.readContainerFile(readCtx, req.ContainerID, agentMDPath)

	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	todayPath := path.Join(memoryDir, "daily", today+".md")
	yesterdayPath := path.Join(memoryDir, "daily", yesterday+".md")
	todayLog, _ := o.readContainerFile(readCtx, req.ContainerID, todayPath)
	yesterdayLog, _ := o.readContainerFile(readCtx, req.ContainerID, yesterdayPath)

	if agentMD == "" && todayLog == "" && yesterdayLog == "" {
		return ""
	}

	var b strings.Builder
	totalChars := 0

	b.WriteString("[AGENT MEMORY]\n")

	if agentMD != "" {
		section := fmt.Sprintf("--- AGENT.md (long-term memory) ---\n%s\n", agentMD)
		if totalChars+len(section) > budget {
			section = section[:budget-totalChars] + "\n...(truncated)"
		}
		b.WriteString(section)
		totalChars += len(section)
	}

	if yesterdayLog != "" && totalChars < budget {
		section := fmt.Sprintf("\n--- Daily log: %s (yesterday) ---\n%s\n", yesterday, yesterdayLog)
		if totalChars+len(section) > budget {
			remaining := budget - totalChars
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

	if todayLog != "" && totalChars < budget {
		section := fmt.Sprintf("\n--- Daily log: %s (today) ---\n%s\n", today, todayLog)
		if totalChars+len(section) > budget {
			remaining := budget - totalChars
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
	return b.String()
}

// buildCrewMemoryBlock reads crew shared memory files and returns a formatted
// block with [CREW SHARED MEMORY] markers. Returns empty string and 0 chars used
// if no crew memory files exist.
func (o *Orchestrator) buildCrewMemoryBlock(ctx context.Context, req AgentRunRequest, budget int, today string) (string, int) {
	crewMemDir := "/crew/shared/.memory"
	crewMDPath := path.Join(crewMemDir, "CREW.md")
	crewDailyPath := path.Join(crewMemDir, "daily", today+".md")

	readCtx, cancel := context.WithTimeout(ctx, memoryReadTimeout)
	defer cancel()

	crewMD, _ := o.readContainerFile(readCtx, req.ContainerID, crewMDPath)
	crewDaily, _ := o.readContainerFile(readCtx, req.ContainerID, crewDailyPath)

	if crewMD == "" && crewDaily == "" {
		return "", 0
	}

	var b strings.Builder
	totalChars := 0

	b.WriteString("[CREW SHARED MEMORY]\n")

	if crewMD != "" {
		section := fmt.Sprintf("--- CREW.md (crew-wide knowledge) ---\n%s\n", crewMD)
		if totalChars+len(section) > budget {
			section = section[:budget-totalChars] + "\n...(truncated)"
		}
		b.WriteString(section)
		totalChars += len(section)
	}

	if crewDaily != "" && totalChars < budget {
		section := fmt.Sprintf("\n--- Crew daily: %s ---\n%s\n", today, crewDaily)
		if totalChars+len(section) > budget {
			remaining := budget - totalChars
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

	b.WriteString("[END CREW SHARED MEMORY]\n\n")
	return b.String(), totalChars
}

// buildMemoryInstructions returns the instruction block that teaches the agent
// how to use persistent memory, including crew shared memory.
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

CREW SHARED MEMORY:
- Crew-wide knowledge is stored at /crew/shared/.memory/
- CREW.md: crew-level decisions, conventions, and shared context (Lead maintains).
- /crew/shared/.memory/daily/{date}.md: crew daily log.
- /crew/shared/.memory/topics/*.md: domain-specific crew knowledge.
- Search crew memory: curl $SIDECAR/memory/search with {"scope":"crew"} or {"scope":"both"}.
- If you are the Lead: write important crew decisions to /crew/shared/.memory/CREW.md.
- If you are an Agent: read crew memory for context. Write personal notes to YOUR agent memory.
- Do not duplicate facts across agent and crew memory.
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
