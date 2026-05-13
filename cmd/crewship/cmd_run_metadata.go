package main

import (
	"fmt"
	"strings"
)

// Latches for AI-first features that need to influence chat-creation /
// stream-event handling without expanding the signature of every helper
// in cmd_run.go. The CLI is single-shot per process, so a package-level
// global has no concurrency hazard; this keeps the diff localised.
var (
	// effortMode holds the value of --effort (minimal|low|medium|high|xhigh).
	// Empty means "don't send" (server picks default).
	effortMode string

	// showThinking surfaces reasoning blocks on stdout (not truncated)
	// when set via --show-thinking on run/ask.
	showThinking bool
)

// validEffortLevels is the closed set the CLI accepts for --effort.
// "minimal" / "xhigh" mirror OpenAI / Claude's reasoning_effort surface;
// "low/medium/high" is the colloquial subset the docs use.
var validEffortLevels = []string{"minimal", "low", "medium", "high", "xhigh"}

// SetEffort validates and stores the --effort flag value. Returns an
// error on an unknown level so the user gets a typo-friendly message
// instead of a silent fall-through to the server's default.
func SetEffort(level string) error {
	level = strings.ToLower(strings.TrimSpace(level))
	if level == "" {
		effortMode = ""
		return nil
	}
	for _, v := range validEffortLevels {
		if v == level {
			effortMode = level
			return nil
		}
	}
	return fmt.Errorf("invalid --effort %q (allowed: %s)", level, strings.Join(validEffortLevels, ", "))
}

// SetShowThinking toggles the --show-thinking latch.
func SetShowThinking(on bool) {
	showThinking = on
}

// ChatCreationMetadata builds the metadata sub-object included in the
// POST /api/v1/agents/:id/chats body. Plan / effort / show-thinking
// land here together so a single source of truth controls "what extra
// hints did the CLI carry over".
//
// Returns nil when no AI-first fields are active so the body stays
// backwards-compatible with older servers that didn't see a metadata
// key at all.
func ChatCreationMetadata() map[string]any {
	if !planModeRequested && effortMode == "" {
		return nil
	}
	m := map[string]any{}
	if planModeRequested {
		m["plan_mode"] = true
	}
	if effortMode != "" {
		m["effort"] = effortMode
	}
	return m
}

// ChatCreationBody returns the POST /api/v1/agents/:id/chats body for a
// CLI-origin chat, with AI-first metadata folded in when active.
func ChatCreationBody() map[string]any {
	body := map[string]any{
		"mode":   "CHAT",
		"origin": "CLI",
	}
	if md := ChatCreationMetadata(); md != nil {
		body["metadata"] = md
	}
	return body
}
