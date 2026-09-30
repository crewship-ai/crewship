package backup

// Backup contents categories — what a custom backup plan chooses from, the
// one category each needs to be restorable, and which tables and bundle
// sections belong to which.
//
// The admin console draws the same list (components/features/admin/backups/
// backups-model.ts, CATEGORIES); categories_test.go parses that constant and
// fails when the two disagree, so the preview the UI shows while a plan is
// being edited and what the server actually puts in the bundle cannot drift.

import (
	"fmt"
	"sort"
)

// Category keys. The wire spells them exactly like this.
const (
	CategoryAgents   = "agents"   // crews, agents and their settings
	CategoryMemory   = "memory"   // memory and its version history
	CategoryChats    = "chats"    // chats
	CategoryAtt      = "att"      // attachment files
	CategoryRoutines = "routines" // routines, schedules and run history
	CategoryJournal  = "journal"  // journal and checkpoints
	CategoryCreds    = "creds"    // credentials and integrations
	CategoryPages    = "pages"    // pages and their files
	CategoryFiles    = "files"    // crew working files
	CategoryEnv      = "env"      // complete container environments
)

// ContentCategory is one row of the contents list.
type ContentCategory struct {
	Key   string
	Label string
	// Needs is the one category this one cannot be restored without; ""
	// when it stands alone.
	Needs string
}

// ContentCategories is the contents list in display order.
var ContentCategories = []ContentCategory{
	{CategoryAgents, "Crews, agents and their settings", ""},
	{CategoryMemory, "Memory and its version history", CategoryAgents},
	{CategoryChats, "Chats", ""},
	{CategoryAtt, "Attachment files", CategoryChats},
	{CategoryRoutines, "Routines, schedules and run history", CategoryAgents},
	{CategoryJournal, "Journal and checkpoints", ""},
	{CategoryCreds, "Credentials and integrations", CategoryAgents},
	{CategoryPages, "Pages and their files", ""},
	{CategoryFiles, "Crew working files", ""},
	{CategoryEnv, "Complete container environments", CategoryFiles},
}

// Presets.
const (
	PresetComplete  = "complete"
	PresetWorkspace = "workspace"
	PresetCustom    = "custom"
)

// Environment modes.
const (
	EnvModeFiles    = "files"
	EnvModeComplete = "complete"
)

// IsCategory reports whether k is a known category key.
func IsCategory(k string) bool {
	for _, c := range ContentCategories {
		if c.Key == k {
			return true
		}
	}
	return false
}

func categoryNeeds(k string) string {
	for _, c := range ContentCategories {
		if c.Key == k {
			return c.Needs
		}
	}
	return ""
}

// RequiredCategory is a category kept only because others need it.
type RequiredCategory struct {
	Key     string   `json:"key"`
	Because []string `json:"because"`
}

// ContentsResolution is what a plan's bundles carry: the categories chosen,
// the ones kept as dependencies (and who needs them), and the rest.
type ContentsResolution struct {
	Included []string           `json:"included"`
	Required []RequiredCategory `json:"required"`
	Excluded []string           `json:"excluded"`
}

// Carried is included + required, in display order.
func (r ContentsResolution) Carried() []string {
	on := map[string]bool{}
	for _, k := range r.Included {
		on[k] = true
	}
	for _, q := range r.Required {
		on[q.Key] = true
	}
	var out []string
	for _, c := range ContentCategories {
		if on[c.Key] {
			out = append(out, c.Key)
		}
	}
	return out
}

// ResolveContents applies the rule the console's resolveContents applies:
// a preset other than custom includes every category; a custom plan includes
// what it lists. Complete container environments follow envMode, never the
// list. Whatever an included (or required) category needs is kept,
// transitively, and records which categories need it. Unknown keys are an
// error.
func ResolveContents(preset string, contents []string, envMode string) (ContentsResolution, error) {
	chosen := map[string]bool{}
	for _, k := range contents {
		if !IsCategory(k) {
			return ContentsResolution{}, fmt.Errorf("unknown contents category %q", k)
		}
		chosen[k] = true
	}
	on := func(k string) bool {
		if k == CategoryEnv {
			return envMode == EnvModeComplete
		}
		return preset != PresetCustom || chosen[k]
	}
	kept := map[string]bool{}
	var queue []string
	for _, c := range ContentCategories {
		if on(c.Key) {
			kept[c.Key] = true
			queue = append(queue, c.Key)
		}
	}
	neededBy := map[string][]string{}
	for len(queue) > 0 {
		k := queue[0]
		queue = queue[1:]
		dep := categoryNeeds(k)
		if dep == "" {
			continue
		}
		neededBy[dep] = append(neededBy[dep], k)
		if !kept[dep] {
			kept[dep] = true
			queue = append(queue, dep)
		}
	}
	res := ContentsResolution{Included: []string{}, Required: []RequiredCategory{}, Excluded: []string{}}
	for _, c := range ContentCategories {
		switch {
		case on(c.Key):
			res.Included = append(res.Included, c.Key)
		case kept[c.Key]:
			res.Required = append(res.Required, RequiredCategory{Key: c.Key, Because: neededBy[c.Key]})
		default:
			res.Excluded = append(res.Excluded, c.Key)
		}
	}
	return res, nil
}

// categoryAnchor marks the tables every bundle carries whatever it holds:
// the workspace row itself and the global identities rows point at.
const categoryAnchor = "anchor"

// categoryWorkOnly marks tables with no category of their own — issues,
// inbox, approvals, labels and the like. They ride every full bundle and no
// custom one.
const categoryWorkOnly = ""

// TableCategory maps every table in BackupTables to its category. A table
// missing here fails TestTableCategory_CoversEveryBackupTable.
var TableCategory = map[string]string{
	"users": categoryAnchor, "workspaces": categoryAnchor, "skills": categoryAnchor,

	// Crews, agents and their settings.
	"crews": CategoryAgents, "service_runtime_intents": CategoryAgents, "crew_members": CategoryAgents,
	"agents": CategoryAgents, "crew_connections": CategoryAgents, "crew_mcp_servers": CategoryAgents,
	"agent_skills": CategoryAgents, "agent_mcp_bindings": CategoryAgents, "agent_config_history": CategoryAgents,
	"crew_templates": CategoryAgents, "workspace_members": CategoryAgents, "access_grants": CategoryAgents,
	"workspace_invitations": CategoryAgents, "workspace_mcp_servers": CategoryAgents,
	"feature_flag_overrides": CategoryAgents, "budget_limits": CategoryAgents, "hooks_config": CategoryAgents,
	"automations": CategoryAgents, "keeper_governance_settings": CategoryAgents, "retention_settings": CategoryAgents,
	"notification_channels": CategoryAgents, "user_notification_prefs": CategoryAgents,
	"notification_channel_agents": CategoryAgents, "notification_templates": CategoryAgents,
	"port_exposures": CategoryAgents, "backup_destinations": CategoryAgents, "onboarding_proposals": CategoryAgents,

	// Memory and its version history.
	"memory_relations": CategoryMemory, "memory_proposals": CategoryMemory, "memory_versions": CategoryMemory,
	"memory_health_snapshots": CategoryMemory, "peer_cards": CategoryMemory, "user_peer_consent": CategoryMemory,
	"user_models": CategoryMemory, "user_model_provenance": CategoryMemory,

	// Chats.
	"chats": CategoryChats, "chat_branches": CategoryChats, "message_reactions": CategoryChats,
	"message_feedback": CategoryChats, "chat_participants": CategoryChats, "chat_read_cursors": CategoryChats,
	"captain_chats": CategoryChats, "workspace_conversations": CategoryChats,
	"workspace_conversation_direct_pairs": CategoryChats, "workspace_conversation_members": CategoryChats,
	"workspace_conversation_messages": CategoryChats, "workspace_conversation_agents": CategoryChats,
	"workspace_conversation_agent_jobs": CategoryChats, "workspace_conversation_outbox": CategoryChats,
	"workspace_conversation_activity": CategoryChats, "workspace_conversation_continuations": CategoryChats,

	// Attachment files (the rows; the blobs are the attachment-blobs section).
	"attachments": CategoryAtt,

	// Routines, schedules and run history.
	"pipelines": CategoryRoutines, "pipeline_versions": CategoryRoutines, "pipeline_drafts": CategoryRoutines,
	"pipeline_schedules": CategoryRoutines, "pipeline_webhooks": CategoryRoutines, "pipeline_runs": CategoryRoutines,
	"pipeline_step_executions": CategoryRoutines, "pipeline_run_artifacts": CategoryRoutines,
	"pipeline_run_step_outputs": CategoryRoutines, "pipeline_routine_state": CategoryRoutines,
	"pending_runs": CategoryRoutines, "pipeline_tags": CategoryRoutines, "pipeline_waitpoints": CategoryRoutines,
	"waitpoint_trust_grants": CategoryRoutines, "run_tags": CategoryRoutines, "routine_step_overrides": CategoryRoutines,
	"scheduled_jobs": CategoryRoutines, "recurring_issues": CategoryRoutines, "workflow_templates": CategoryRoutines,
	"workflow_states": CategoryRoutines, "eval_runs": CategoryRoutines, "gate_reward_history": CategoryRoutines,

	// Journal and checkpoints.
	"journal_entries": CategoryJournal, "journal_entry_priorities": CategoryJournal,
	"journal_chain_checkpoints": CategoryJournal, "checkpoints": CategoryJournal,
	"agent_session_checkpoints": CategoryJournal, "skill_invocations": CategoryJournal,
	"cost_ledger": CategoryJournal, "gdpr_actions": CategoryJournal,

	// Credentials and integrations.
	"credentials": CategoryCreds, "credential_crews": CategoryCreds, "credential_bindings": CategoryCreds,
	"credential_fields": CategoryCreds, "provider_login_pools": CategoryCreds,
	"provider_login_pool_members": CategoryCreds, "agent_credentials": CategoryCreds,
	"credential_audit": CategoryCreds, "credential_rotations": CategoryCreds, "composio_settings": CategoryCreds,

	// Pages and their files.
	"page_folders": CategoryPages, "page_folder_acl": CategoryPages, "pages": CategoryPages,
	"page_panels": CategoryPages, "page_panel_data": CategoryPages, "page_versions": CategoryPages,
	"page_project_builds": CategoryPages, "page_project_publications": CategoryPages,
	"page_project_live": CategoryPages, "page_project_withdrawals": CategoryPages,
	"page_project_drafts": CategoryPages, "page_project_revisions": CategoryPages, "page_grants": CategoryPages,
	"page_public_tokens": CategoryPages, "page_webhooks": CategoryPages, "page_panel_alerts": CategoryPages,

	// Crew working files (the workspace file index; the files themselves are
	// the containers' sections).
	"workspace_files": CategoryFiles,

	// Work items: full bundles only.
	"missions": categoryWorkOnly, "issue_work": categoryWorkOnly, "issue_executions": categoryWorkOnly,
	"issue_counters": categoryWorkOnly, "issue_agent_sessions": categoryWorkOnly, "assignments": categoryWorkOnly,
	"approvals_queue": categoryWorkOnly, "mission_tasks": categoryWorkOnly, "mission_activity": categoryWorkOnly,
	"mission_comments": categoryWorkOnly, "mission_comment_mentions": categoryWorkOnly,
	"mission_labels": categoryWorkOnly, "mission_proposals": categoryWorkOnly, "mission_relations": categoryWorkOnly,
	"mission_code_links": categoryWorkOnly, "labels": categoryWorkOnly, "milestones": categoryWorkOnly,
	"projects": categoryWorkOnly, "subscriptions": categoryWorkOnly, "inbox_items": categoryWorkOnly,
	"inbox_item_reads": categoryWorkOnly, "triage_rules": categoryWorkOnly, "saved_views": categoryWorkOnly,
}

// Crew container sections (collector.go) and the category each belongs to.
const (
	SectionCrewWorkspace = "workspace"
	SectionCrewMemory    = "crew"
	SectionCrewOutput    = "output"
	SectionCrewHome      = "home"
	SectionCrewTools     = "tools"
	SectionCrewVarLib    = "var-lib"
)

var sectionCategory = map[string]string{
	SectionCrewWorkspace: CategoryFiles,
	SectionCrewMemory:    CategoryMemory,
	SectionCrewOutput:    CategoryFiles,
	SectionCrewHome:      CategoryFiles,
	SectionCrewTools:     CategoryFiles,
	SectionCrewVarLib:    CategoryFiles,
}

// categoryFilter answers "does this bundle carry X?". A nil filter carries
// everything (a full bundle).
type categoryFilter map[string]bool

func newCategoryFilter(categories []string) (categoryFilter, error) {
	if len(categories) == 0 {
		return nil, nil
	}
	f := categoryFilter{}
	for _, k := range categories {
		if !IsCategory(k) {
			return nil, fmt.Errorf("backup: unknown contents category %q", k)
		}
		f[k] = true
	}
	return f, nil
}

func (f categoryFilter) has(category string) bool {
	return f == nil || f[category]
}

func (f categoryFilter) table(name string) bool {
	if f == nil {
		return true
	}
	cat, ok := TableCategory[name]
	if !ok || cat == categoryWorkOnly {
		return false
	}
	return cat == categoryAnchor || f[cat]
}

func (f categoryFilter) section(name string) bool {
	if f == nil {
		return true
	}
	return f[sectionCategory[name]]
}

// filterDump drops the tables a custom bundle does not carry. Returns the
// dropped table names, sorted.
func (f categoryFilter) filterDump(d *DBDump) []string {
	if f == nil || d == nil {
		return nil
	}
	var dropped []string
	for name := range d.Tables {
		if !f.table(name) {
			delete(d.Tables, name)
			dropped = append(dropped, name)
		}
	}
	sort.Strings(dropped)
	return dropped
}

// sorted returns the filter's categories in display order.
func (f categoryFilter) sorted() []string {
	if f == nil {
		return nil
	}
	var out []string
	for _, c := range ContentCategories {
		if f[c.Key] {
			out = append(out, c.Key)
		}
	}
	return out
}
