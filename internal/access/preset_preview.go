package access

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
)

var ErrPresetInvalid = errors.New("invalid preset selection")

type PresetResource struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}
type PresetRequest struct {
	MembershipID string         `json:"membership_id"`
	Revision     int64          `json:"revision"`
	Resource     PresetResource `json:"resource"`
	PresetID     string         `json:"preset_id"`
	Version      int            `json:"preset_version"`
	Level        string         `json:"level"`
	Reissue      []Right        `json:"reissue"`
}
type PresetChange struct {
	Right     Right  `json:"right"`
	Action    string `json:"action"`
	IssuerID  string `json:"issuer_id"`
	CreatedAt string `json:"created_at,omitempty"`
	Live      bool   `json:"live"`
}
type PresetWarning struct {
	Code           string `json:"code"`
	Message        string `json:"message"`
	BeforeEligible *bool  `json:"before_eligible,omitempty"`
	AfterEligible  *bool  `json:"after_eligible,omitempty"`
}
type PresetPreview struct {
	Selection        PresetRequest    `json:"selection"`
	DefinitionHash   string           `json:"definition_hash"`
	CurrentSelection *PresetSelection `json:"current_selection"`
	Before           []GrantDetail    `json:"before"`
	Changes          []PresetChange   `json:"changes"`
	AfterRights      []Right          `json:"after_rights"`
	OrthogonalRights []GrantDetail    `json:"orthogonal_rights"`
	Warnings         []PresetWarning  `json:"warnings"`
	Digest           string           `json:"confirmation_digest"`
}
type CatalogEntry struct {
	Definition PresetDefinition `json:"definition"`
	Hash       string           `json:"definition_hash"`
}

func (s Store) PresetCatalogIn(ctx context.Context, tx *sql.Tx, actor, workspace string) ([]CatalogEntry, error) {
	if tx == nil {
		return nil, ErrDenied
	}
	if err := operator(ctx, tx, actor, workspace); err != nil {
		return nil, err
	}
	entries := []CatalogEntry{}
	for _, d := range PresetCatalog() {
		entries = append(entries, CatalogEntry{d, d.Hash()})
	}
	return entries, nil
}

// PlanPresetIn reads one caller-owned snapshot. It never writes grants, choice,
// revision, timestamps or audit. The digest is confirmation data, not authority.
func (s Store) PlanPresetIn(ctx context.Context, tx *sql.Tx, actor, user, workspace string, req PresetRequest) (PresetPreview, error) {
	var result PresetPreview
	if tx == nil {
		return result, ErrDenied
	}
	p, err := s.PolicyIn(ctx, tx, actor, user, workspace)
	if err != nil {
		return result, err
	}
	if req.MembershipID != p.ID || req.Revision != p.Revision {
		return result, ErrConflict
	}
	d, known := ResolvePreset(req.PresetID, req.Version)
	ops, levelKnown := d.Operations(req.Level)
	if !known || !levelKnown || req.Resource.Kind != d.Kind || req.Resource.ID == "" || p.Mode != "restricted" {
		return result, ErrPresetInvalid
	}
	var administrator bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM workspace_members WHERE user_id=? AND role IN ('OWNER','ADMIN'))`, user).Scan(&administrator); err != nil {
		return result, err
	}
	if administrator {
		return result, ErrDenied
	}
	if err = resourceExists(ctx, tx, workspace, Right{req.Resource.Kind, req.Resource.ID, ops[0]}); err != nil {
		return result, err
	}
	// Bind independent role/capability floors and account liveness, even when
	// changing them does not happen to advance this target's access revision.
	identity := func(id string) (string, error) {
		var snapshot string
		err := tx.QueryRowContext(ctx, `SELECT json_object('membership_id',wm.id,'role',wm.role,'capabilities',wm.capabilities,'mode',wm.access_mode,'revision',wm.access_revision,'suspended_at',u.suspended_at) FROM workspace_members wm JOIN users u ON u.id=wm.user_id WHERE wm.user_id=? AND wm.workspace_id=?`, id, workspace).Scan(&snapshot)
		return snapshot, err
	}
	actorSnapshot, err := identity(actor)
	if err != nil {
		return result, err
	}
	targetSnapshot, err := identity(user)
	if err != nil {
		return result, err
	}
	var resourceSnapshot, role string
	query := `SELECT json_object('id',id,'updated_at',updated_at) FROM projects WHERE id=? AND workspace_id=?`
	if d.Kind == "routine" {
		query = `SELECT json_object('id',id,'publication_revision',publication_revision,'definition_hash',definition_hash,'status',status) FROM pipelines WHERE id=? AND workspace_id=? AND deleted_at IS NULL`
	}
	if err = tx.QueryRowContext(ctx, query, req.Resource.ID, workspace).Scan(&resourceSnapshot); err != nil {
		return result, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT role FROM workspace_members WHERE id=?`, p.ID).Scan(&role); err != nil {
		return result, err
	}
	managed := func(r Right) bool {
		return r.Kind == d.Kind && r.ID == req.Resource.ID && slices.Contains(d.ManagedOperations, r.Operation)
	}
	before, desired, renew := map[Right]GrantDetail{}, map[Right]bool{}, map[Right]bool{}
	result = PresetPreview{Selection: req, DefinitionHash: d.Hash(), Before: append([]GrantDetail{}, p.GrantDetails...), Changes: []PresetChange{}, AfterRights: []Right{}, OrthogonalRights: []GrantDetail{}, Warnings: []PresetWarning{}}
	for _, g := range p.GrantDetails {
		before[g.Right] = g
		if !managed(g.Right) {
			desired[g.Right] = true
			result.OrthogonalRights = append(result.OrthogonalRights, g)
		}
	}
	for _, op := range ops {
		desired[Right{d.Kind, req.Resource.ID, op}] = true
	}
	if len(desired) > 256 {
		return result, ErrPresetInvalid
	}
	for _, r := range req.Reissue {
		if _, retained := before[r]; !retained || !managed(r) || !desired[r] || renew[r] {
			return result, ErrPresetInvalid
		}
		renew[r] = true
	}
	less := func(a, b Right) int {
		if a.Kind != b.Kind {
			if a.Kind < b.Kind {
				return -1
			}
			return 1
		}
		if a.ID != b.ID {
			if a.ID < b.ID {
				return -1
			}
			return 1
		}
		if a.Operation < b.Operation {
			return -1
		}
		if a.Operation > b.Operation {
			return 1
		}
		return 0
	}
	result.Selection.Reissue = slices.Clone(req.Reissue)
	if result.Selection.Reissue == nil {
		result.Selection.Reissue = []Right{}
	}
	slices.SortFunc(result.Selection.Reissue, less)
	union := map[Right]bool{}
	for r := range desired {
		// Orthogonal canonical rows may be inactive historical grants. They
		// confer no new authority and must be preserved without revalidation.
		if managed(r) {
			if err := resourceExists(ctx, tx, workspace, r); err != nil {
				return result, err
			}
		}
		result.AfterRights = append(result.AfterRights, r)
		if managed(r) {
			union[r] = true
		}
	}
	for r := range before {
		if managed(r) {
			union[r] = true
		}
	}
	slices.SortFunc(result.AfterRights, less)
	keys := []Right{}
	for r := range union {
		keys = append(keys, r)
	}
	slices.SortFunc(keys, less)
	for _, r := range keys {
		g, existed := before[r]
		change := PresetChange{r, "retain", g.IssuerID, g.CreatedAt, g.Live}
		switch {
		case !desired[r]:
			change.Action = "remove"
		case !existed || renew[r]:
			change.Action, change.IssuerID, change.CreatedAt, change.Live = "add", actor, "", true
			if renew[r] {
				change.Action = "reissue"
			}
		}
		result.Changes = append(result.Changes, change)
	}
	selections, err := s.PresetSelectionsIn(ctx, tx, actor, user, workspace)
	if err != nil {
		return result, err
	}
	for _, choice := range selections {
		if choice.Kind == d.Kind && choice.ResourceID == req.Resource.ID {
			result.CurrentSelection = &choice
		}
	}
	live := func(op string, after bool) bool {
		r := Right{d.Kind, req.Resource.ID, op}
		g, exists := before[r]
		if !after {
			return exists && g.Live
		}
		return desired[r] && (!exists || renew[r] || g.Live)
	}
	if d.Kind == "routine" {
		result.Warnings = append(result.Warnings, PresetWarning{Code: "ROUTINE_RESULTS_HISTORY", Message: "View with Results includes eligible past and future regular outputs, possibly input material; live issuer and private-audience gates still apply."})
		if slices.Contains(ops, "run") {
			result.Warnings = append(result.Warnings, PresetWarning{Code: "EXECUTION_GATES", Message: "Run also needs current agent, provider, delegation and runtime authority; this preview does not execute work."})
		}
		if slices.Contains(ops, "edit") {
			result.Warnings = append(result.Warnings, PresetWarning{Code: "DRAFT_ONLY", Message: "Edit allows draft changes, never publication."})
		}
	} else if g, retained := before[Right{"project", req.Resource.ID, "delete_issues"}]; retained {
		was, will := g.Live && live("read", false) && live("manage", false), g.Live && live("read", true) && live("manage", true)
		result.Warnings = append(result.Warnings, PresetWarning{Code: "RETAINED_DELETE_ISSUES", Message: "The separate destructive right remains unchanged; Read and Manage affect eligibility. Disposable-work and active-execution guards still apply.", BeforeEligible: &was, AfterEligible: &will})
	}
	if d.Kind == "project" && role != "MANAGER" && role != "ADMIN" && role != "OWNER" {
		for _, g := range result.OrthogonalRights {
			if g.Kind == d.Kind && g.ID == req.Resource.ID && (g.Operation == "write" || g.Operation == "delete") {
				result.Warnings = append(result.Warnings, PresetWarning{Code: "FILE_ROLE_FLOOR", Message: "Retained source-file write/delete rights still require MANAGER or higher; selecting a preset does not change the role."})
				break
			}
		}
	}
	// Names are presentation, not authority, and must not invalidate consent.
	bound := result
	bound.Before = slices.Clone(result.Before)
	bound.OrthogonalRights = slices.Clone(result.OrthogonalRights)
	for i := range bound.Before {
		bound.Before[i].IssuerName = ""
	}
	for i := range bound.OrthogonalRights {
		bound.OrthogonalRights[i].IssuerName = ""
	}
	encoded, err := json.Marshal([]any{workspace, actor, actorSnapshot, targetSnapshot, resourceSnapshot, bound})
	if err != nil {
		return result, err
	}
	sum := sha256.Sum256(encoded)
	result.Digest = hex.EncodeToString(sum[:])
	return result, nil
}
