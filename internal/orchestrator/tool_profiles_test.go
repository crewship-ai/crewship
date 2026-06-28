package orchestrator

import (
	"strings"
	"testing"
)

// TestBuiltinToolAllowlist locks the curated built-in tool surface per profile.
// The whole point is that an agent must NEVER inherit Claude Code's
// harness-internal tools (TaskCreate/ToolSearch/Agent/Workflow/Cron*/…) — those
// have no Crewship backing, so an agent calling them writes to ephemeral
// in-process state and can't say where the data went. (That's the bug this
// fixes: an agent "created a task" that went nowhere.)
func TestBuiltinToolAllowlist(t *testing.T) {
	// Tools that must NEVER appear in any profile — harness-internal /
	// interactive tools with no Crewship meaning in a headless agent.
	forbidden := []string{
		"Task", "TaskCreate", "TaskUpdate", "TaskList", "TaskGet", "TaskStop",
		"TaskOutput", "TodoWrite", "ToolSearch", "Agent", "Workflow",
		"CronCreate", "CronDelete", "CronList", "ScheduleWakeup", "RemoteTrigger",
		"EnterPlanMode", "ExitPlanMode", "AskUserQuestion", "Artifact",
		"SendMessage", "Skill", "Monitor", "PushNotification",
	}

	for _, profile := range []string{"MINIMAL", "CODING", "FULL", "", "BOGUS"} {
		got := builtinToolAllowlist(profile)
		if len(got) == 0 {
			t.Fatalf("profile %q: allowlist must never be empty (empty would disable all tools)", profile)
		}
		set := map[string]bool{}
		for _, name := range got {
			if name == "" {
				t.Errorf("profile %q: allowlist contains an empty tool name", profile)
			}
			if set[name] {
				t.Errorf("profile %q: duplicate tool %q", profile, name)
			}
			set[name] = true
		}
		for _, bad := range forbidden {
			if set[bad] {
				t.Errorf("profile %q: forbidden harness tool %q present in allowlist", profile, bad)
			}
		}
		// "Search" is NOT a real Claude Code tool name — guard the old bug.
		if set["Search"] {
			t.Errorf("profile %q: 'Search' is not a real built-in tool (should be Glob/Grep/WebSearch)", profile)
		}
		// Read is the floor — every profile can at least read.
		if !set["Read"] {
			t.Errorf("profile %q: Read must always be allowed", profile)
		}
	}

	// MINIMAL is read-only: no Write/Edit/Bash.
	min := toSet(builtinToolAllowlist("MINIMAL"))
	for _, w := range []string{"Write", "Edit", "Bash"} {
		if min[w] {
			t.Errorf("MINIMAL must be read-only, but allows %q", w)
		}
	}
	for _, r := range []string{"Read", "Glob", "Grep"} {
		if !min[r] {
			t.Errorf("MINIMAL must allow %q", r)
		}
	}

	// CODING escalates MINIMAL with write + exec.
	coding := toSet(builtinToolAllowlist("CODING"))
	for _, w := range []string{"Write", "Edit", "Bash"} {
		if !coding[w] {
			t.Errorf("CODING must allow %q", w)
		}
	}

	// Unknown / empty profile falls back to CODING (the DB default).
	if strings.Join(builtinToolAllowlist(""), ",") != strings.Join(builtinToolAllowlist("CODING"), ",") {
		t.Error("empty profile must fall back to the CODING allowlist")
	}

	// FULL is a superset of CODING.
	full := toSet(builtinToolAllowlist("FULL"))
	for name := range coding {
		if !full[name] {
			t.Errorf("FULL must be a superset of CODING, missing %q", name)
		}
	}
}

func toSet(in []string) map[string]bool {
	m := make(map[string]bool, len(in))
	for _, s := range in {
		m[s] = true
	}
	return m
}
