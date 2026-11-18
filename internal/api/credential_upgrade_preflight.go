package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/crewship-ai/crewship/internal/orchestrator"
)

type CredentialUpgradeAgent struct {
	ID                               string                               `json:"agent_id" yaml:"agent_id"`
	Slug                             string                               `json:"agent_slug" yaml:"agent_slug"`
	Problems                         []orchestrator.CredentialNameProblem `json:"problems" yaml:"problems"`
	Warnings                         []string                             `json:"warnings" yaml:"warnings"`
	LegacySandboxBlocked             bool                                 `json:"legacy_sandbox_blocked" yaml:"legacy_sandbox_blocked"`
	crew, adapter, agentMCP, crewMCP string
}

type CredentialUpgradeReport struct {
	// Candidate provenance is supplied by the offline CLI inspector. The
	// metadata loader itself never infers a running build from its shell.
	SourceBuild        string `json:"source_build" yaml:"source_build"`
	TargetBuild        string `json:"target_build" yaml:"target_build"`
	ConfigurationKnown bool   `json:"configuration_known" yaml:"configuration_known"`
	TargetBuildKnown   bool   `json:"target_build_known" yaml:"target_build_known"`

	Ready               bool                     `json:"ready" yaml:"ready"`
	State               string                   `json:"state" yaml:"state"`
	WorkspaceID         string                   `json:"workspace_id" yaml:"workspace_id"`
	Scope               string                   `json:"scope" yaml:"scope"`
	Agents              []CredentialUpgradeAgent `json:"agents" yaml:"agents"`
	CodexSandboxMode    string                   `json:"codex_sandbox_mode" yaml:"codex_sandbox_mode"`
	ConfigurationSource string                   `json:"configuration_source" yaml:"configuration_source"`
}

// InspectCredentialUpgrade is an offline, metadata-only consumer of the exact
// resolver snapshot. It never opens encrypted values or runs migrations. The
// caller must open the retained database in SQLite read-only mode.
func InspectCredentialUpgrade(ctx context.Context, db *sql.DB, workspace string, keeper bool, sandbox string) (*CredentialUpgradeReport, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var ws string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM workspaces WHERE id=? OR slug=?`, workspace, workspace).Scan(&ws); err != nil {
		return nil, errors.New("workspace unavailable in retained database")
	}
	r := &CredentialUpgradeReport{Ready: true, State: "PASS", WorkspaceID: ws, Scope: "potential native credential alias and legacy Codex configuration conflicts; not payload validation or authentication", Agents: []CredentialUpgradeAgent{}, CodexSandboxMode: sandbox, ConfigurationSource: "operator-declared running server configuration"}
	// Older installations have no per-crew opt-in column. Absence means no
	// consent; never infer consent from the legacy global environment setting.
	columns, err := tx.QueryContext(ctx, `PRAGMA table_info(crews)`)
	if err != nil {
		return nil, err
	}
	hasConsent := false
	for columns.Next() {
		var cid, notnull, pk int
		var name, kind string
		var def any
		if err := columns.Scan(&cid, &name, &kind, &notnull, &def, &pk); err != nil {
			columns.Close()
			return nil, err
		}
		if name == "allow_codex_outer_sandbox" {
			hasConsent = true
		}
	}
	if err := columns.Err(); err != nil {
		columns.Close()
		return nil, err
	}
	columns.Close()
	consent := "0"
	if hasConsent {
		consent = "COALESCE(c.allow_codex_outer_sandbox,0)"
	}
	rows, err := tx.QueryContext(ctx, `SELECT a.id,a.slug,COALESCE(a.crew_id,''),a.cli_adapter,COALESCE(a.mcp_config_json,''),COALESCE(c.mcp_config_json,''),`+consent+` FROM agents a LEFT JOIN crews c ON c.id=a.crew_id AND c.workspace_id=a.workspace_id AND c.deleted_at IS NULL WHERE a.workspace_id=? AND a.deleted_at IS NULL ORDER BY a.id`, ws)
	if err != nil {
		return nil, errors.New("retained agent schema cannot be inspected by this candidate; no upgrade readiness established")
	}
	for rows.Next() {
		a := CredentialUpgradeAgent{Problems: []orchestrator.CredentialNameProblem{}, Warnings: []string{}}
		var allowed bool
		if err := rows.Scan(&a.ID, &a.Slug, &a.crew, &a.adapter, &a.agentMCP, &a.crewMCP, &allowed); err != nil {
			rows.Close()
			return nil, err
		}
		a.LegacySandboxBlocked = sandbox == "danger-full-access" && a.adapter == "CODEX_CLI" && !allowed
		r.Agents = append(r.Agents, a)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for i := range r.Agents {
		a := &r.Agents[i]
		delivered, notices, err := loadDeliveredCredentialSnapshot(ctx, tx, a.ID)
		if err != nil {
			return nil, errors.New("retained credential schema or metadata cannot be resolved; no upgrade readiness established")
		}
		if len(notices) > 0 {
			a.Warnings = append(a.Warnings, "resolver renamed or withheld credential slots; inspect native credential resolve notices")
		}
		configs := []string{a.agentMCP, a.crewMCP}
		// Conservatively include enabled workspace/crew table definitions. A
		// shadowed definition can over-report a potential conflict, never hide it.
		for _, spec := range []struct{ query, arg string }{{`SELECT env_json FROM workspace_mcp_servers WHERE workspace_id=? AND enabled=1 AND deleted_at IS NULL`, ws}, {`SELECT env_json FROM crew_mcp_servers WHERE crew_id=? AND enabled=1 AND deleted_at IS NULL`, a.crew}} {
			mrows, err := tx.QueryContext(ctx, spec.query, spec.arg)
			if err != nil {
				a.Warnings = append(a.Warnings, "MCP table metadata unavailable; reference coverage unknown")
				continue
			}
			for mrows.Next() {
				var env sql.NullString
				if err := mrows.Scan(&env); err != nil {
					mrows.Close()
					return nil, err
				}
				if strings.TrimSpace(env.String) != "" {
					configs = append(configs, `{"mcpServers":{"metadata":{"env":`+env.String+`}}}`)
				}
			}
			if err := mrows.Err(); err != nil {
				mrows.Close()
				return nil, err
			}
			mrows.Close()
		}
		refs, refsErr := orchestrator.CredentialUpgradeMCPReferences(configs...)
		if refsErr != nil {
			a.Warnings = append(a.Warnings, "malformed typed MCP metadata; reference coverage unknown")
		}
		var bindings int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_mcp_bindings WHERE agent_id=? AND enabled=1 AND (COALESCE(credential_id,'')!='' OR COALESCE(env_var_name,'')!='' OR COALESCE(config_override_json,'') NOT IN ('','{}','null'))`, a.ID).Scan(&bindings); err != nil {
			a.Warnings = append(a.Warnings, "agent MCP binding metadata unavailable; coverage unknown")
		} else if bindings > 0 {
			a.Warnings = append(a.Warnings, "additional agent MCP credential bindings or overrides require further metadata inspection; coverage unknown")
		}
		covered := map[string]bool{}
		creds := []orchestrator.Credential{}
		for _, d := range delivered {
			c := orchestrator.Credential{ID: d.ID, EnvVarName: d.EnvVar, Type: d.Type, Provider: d.Provider, HandleOnly: d.HandleOnly}
			for _, field := range d.Fields {
				c.Fields = append(c.Fields, orchestrator.CredentialField{EnvVar: field.EnvVar, IsSecret: field.IsSecret})
			}
			creds = append(creds, c)
			covered[d.EnvVar] = true
		}
		for ref := range refs {
			if !covered[ref] && !isReservedMCPEnvVar(ref) {
				a.Warnings = append(a.Warnings, fmt.Sprintf("MCP reference %s may use additional auto-resolved credentials; metadata coverage unknown", ref))
			}
		}
		sort.Strings(a.Warnings)
		a.Problems = orchestrator.InspectCredentialUpgradeNames(creds, keeper, refs)
		if a.LegacySandboxBlocked || len(a.Problems) > 0 {
			r.Ready = false
			r.State = "BLOCKED"
		}
		if len(a.Warnings) > 0 {
			r.Ready = false
			if r.State == "PASS" {
				r.State = "UNKNOWN"
			}
		}
	}
	return r, tx.Commit()
}
