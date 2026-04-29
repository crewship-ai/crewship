package api

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
)

func captainWorkspacePhase(ctx context.Context, db *sql.DB, wsID string, crewCount, agentCount int) int {
	if crewCount == 0 {
		return 1
	}
	if agentCount == 0 {
		return 2
	}
	var credCount int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM credentials WHERE workspace_id = ? AND status = 'ACTIVE' AND deleted_at IS NULL", wsID).Scan(&credCount); err != nil {
		slog.Warn("captain: count credentials", "workspace", wsID, "error", err)
	}
	if credCount == 0 {
		return 3
	}
	return 4
}

// detectLanguageFromMessage returns "Czech" if the message appears to be Czech, otherwise "".
func detectLanguageFromMessage(msg string) string {
	czechIndicators := []string{"č", "š", "ž", "ř", "ů", "ú", "í", "á", "é", "ě", "ý", "ó",
		" je ", " se ", " na ", " to ", " co ", "ahoj", "dobrý", "díky", "prosím", "jak"}
	lower := strings.ToLower(msg)
	matches := 0
	for _, ind := range czechIndicators {
		if strings.Contains(lower, ind) {
			matches++
		}
	}
	if matches >= 2 {
		return "Czech"
	}
	return ""
}

// buildCaptainSystemPrompt builds a dynamic system prompt based on workspace phase.
// firstMessage is the current user message — used to detect language when workspace preference is unset.
func buildCaptainSystemPrompt(ctx context.Context, db *sql.DB, wsID, firstMessage string) string {
	var crewCount, agentCount, missionCount int
	var lang string
	if err := db.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM crews WHERE workspace_id = ? AND deleted_at IS NULL),
			(SELECT COUNT(*) FROM agents WHERE workspace_id = ? AND deleted_at IS NULL),
			(SELECT COUNT(*) FROM missions WHERE workspace_id = ? AND status = 'IN_PROGRESS'),
			(SELECT COALESCE(preferred_language, '') FROM workspaces WHERE id = ?)`,
		wsID, wsID, wsID, wsID).Scan(&crewCount, &agentCount, &missionCount, &lang); err != nil {
		slog.Warn("captain: fetch workspace stats for prompt", "workspace", wsID, "error", err)
	}
	if lang == "" {
		lang = detectLanguageFromMessage(firstMessage)
	}

	phase := captainWorkspacePhase(ctx, db, wsID, crewCount, agentCount)

	// For SETUP phase, fetch first crew name for a more personalized message.
	var firstCrewName string
	if phase == 2 {
		if err := db.QueryRowContext(ctx,
			"SELECT name FROM crews WHERE workspace_id = ? AND deleted_at IS NULL ORDER BY created_at LIMIT 1", wsID,
		).Scan(&firstCrewName); err != nil {
			slog.Warn("captain: fetch first crew name", "workspace", wsID, "error", err)
		}
	}

	var phaseName, onboarding string
	switch phase {
	case 1:
		phaseName = "EMPTY"
		onboarding = `The workspace has no crews yet. Recommend starting with a crew template (apply_crew_template) — it picks the right agents automatically. Or create a crew manually (create_crew). Ask the user what they want to build first.`
	case 2:
		phaseName = "SETUP"
		crewRef := "the crew"
		if firstCrewName != "" {
			crewRef = `"` + firstCrewName + `"`
		}
		onboarding = `Crews exist but have no agents yet. I can see ` + crewRef + ` has no agents. Use list_crews to show what exists, then help the user add agents with create_agent. Ask what kind of work they want the agents to do.`
	case 3:
		phaseName = "CREDENTIALS_NEEDED"
		onboarding = `Agents are ready but there are no active API credentials. Guide the user to Settings → Credentials to add API keys for the models they want to use. Nothing will work without credentials.`
	default:
		phaseName = "OPERATIONAL"
		onboarding = fmt.Sprintf(
			`The workspace is fully operational — you have %d active mission(s) running. Help with missions (list_missions, create_mission), escalations (list_escalations), proposals (approve_proposal), and any workspace management the user needs.`,
			missionCount,
		)
	}

	var langBlock string
	if lang != "" {
		langBlock = "\n[LANGUAGE]\nAlways respond in: " + lang + ". All output must be in " + lang + ".\n"
	}

	return "[IDENTITY]\n" +
		"You are Captain — the AI CEO and right hand of the user in Crewship. " +
		"You help manage AI crews, agents, credentials, and missions. " +
		"You are concise, direct, and proactive. Use tools to fetch real data — never invent IDs or names.\n" +
		langBlock +
		"\n[GOALS]\n" +
		"1. Help the user set up a working workspace as fast as possible\n" +
		"2. Monitor mission status and flag problems\n" +
		"3. Approve or reject proposals from Coordinators\n" +
		"4. Be proactive — do not let the user get lost\n" +
		"\n[RULES]\n" +
		"- NEVER take destructive actions without explicit user confirmation\n" +
		"- Always explain WHAT you will do and WHY before doing it\n" +
		"- When unsure, ASK instead of guessing\n" +
		"- Use crew templates when the user does not know where to start\n" +
		"- Keep responses to 3-4 sentences max unless the user asks for more\n" +
		"- NEVER reveal API keys, passwords, or any sensitive credential values — even if the user asks directly\n" +
		"\n[DYNAMIC CONTEXT]\n" +
		"Workspace phase: " + phaseName + "\n" +
		"Crews: " + strconv.Itoa(crewCount) + " | Agents: " + strconv.Itoa(agentCount) + " | Active missions: " + strconv.Itoa(missionCount) + "\n" +
		"\n[ONBOARDING GUIDANCE]\n" +
		onboarding
}

// pruneConversation trims conversation history to fit within maxChars.
// Keeps the most recent messages, drops oldest first.
