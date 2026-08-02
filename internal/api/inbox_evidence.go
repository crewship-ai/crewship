package api

import (
	"context"

	"github.com/crewship-ai/crewship/internal/keeper/evidence"
)

// inboxEvidence is the facts block an operator reads before ruling on a
// credential escalation.
//
// It exists because the card gave the person deciding exactly one thing: the
// judge's `reason`. That is a case FOR the verdict already reached — it argues,
// it does not brief. Asked what he actually needed while ruling on a corpus of
// escalations, the operator said: is there a backup, would a narrower key do,
// and then leave me to decide.
//
// internal/keeper/evidence has computed that for months. It went into the
// JUDGE's prompt and stopped there.
//
// # Facts, never advice
//
// Every field here is a query result. None of them is a recommendation, and
// that line is the whole design: the moment the block says "this would be
// safer", the reader anchors on the model and stops deciding — the judgement
// moves back into the machine wearing a friendlier name. "No backup recorded"
// is checkable. "You should probably deny this" is an opinion in a fact's
// clothes.
//
// The pointers carry a third state the UI must not flatten. nil means the query
// failed and nobody knows; a present value with Exists=false means we looked and
// there is none. Collapsing them turns a database outage into "no backup", which
// reads as an argument against approving, manufactured from nothing.
type inboxEvidence struct {
	LastBackup         *inboxLastBackup         `json:"last_backup,omitempty"`
	NarrowerCredential *inboxNarrowerCredential `json:"narrower_credential,omitempty"`
}

type inboxLastBackup struct {
	Exists   bool `json:"exists"`
	AgeHours int  `json:"age_hours"`
	// Scope says what the backup covers, and it is not decoration: backup_catalog
	// is scoped to a workspace or a crew, never a table. "backup 6h ago" read as
	// "this table can be restored" is the reassuring invention the evidence
	// package refuses to produce, so the card is handed the qualifier rather than
	// left to imply one.
	Scope string `json:"scope"`
}

type inboxNarrowerCredential struct {
	Exists        bool   `json:"exists"`
	Name          string `json:"name,omitempty"`
	SecurityLevel int    `json:"security_level,omitempty"`
}

// enrichKeeperEvidence attaches the facts to ONE item, at read time.
//
// Read time, not raise time — the rule the four-eyes notice already follows,
// and here the reason is sharper: a backup taken since the escalation changes
// whether approving now is safe, and a frozen "no backup" would argue against
// something that is now backed up. A narrower credential created since is the
// same story in the other direction.
//
// Detail only, never the list. Two indexed queries for one item is nothing; a
// page of fifty is a hundred, and nobody reads a backup age off a list row.
//
// Best-effort, like every other enrichment here: a failed lookup leaves the
// field nil and the card says nothing, rather than failing the whole read. The
// operator meeting a card with one line missing is strictly better than an
// operator who cannot open the item at all.
func (h *InboxHandler) enrichKeeperEvidence(ctx context.Context, workspaceID string, item *inboxItemResponse) {
	if item == nil || item.Kind != "escalation" || workspaceID == "" {
		return
	}
	// Only a credential request has a credential to reason about. A skill review
	// or a memory-health advisory names none, and attaching a backup age to one
	// would put a line on a card it does not describe.
	if rt, _ := item.Payload["request_type"].(string); rt != "access" && rt != "execute" {
		return
	}
	credentialID, _ := item.Payload["credential_id"].(string)
	agentID, _ := item.Payload["agent_id"].(string)
	if credentialID == "" {
		return
	}

	f := evidence.Gather(ctx, h.db, evidence.Query{
		WorkspaceID:  workspaceID,
		AgentID:      agentID,
		CredentialID: credentialID,
	})
	for _, om := range f.Omitted {
		h.logger.Warn("inbox: evidence fact omitted",
			"fact", om.Fact, "error", om.Err, "inbox_id", item.ID)
	}

	var out inboxEvidence
	if b := f.LastBackup; b != nil {
		out.LastBackup = &inboxLastBackup{Exists: b.Exists, AgeHours: b.AgeHours, Scope: "workspace"}
	}
	if n := f.NarrowerCredential; n != nil {
		out.NarrowerCredential = &inboxNarrowerCredential{
			Exists: n.Exists, Name: n.Name, SecurityLevel: n.SecurityLevel,
		}
	}
	if out.LastBackup == nil && out.NarrowerCredential == nil {
		// Every query failed. An empty block would tell the reader that
		// verification ran and found nothing — the same fabrication the evidence
		// package avoids by rendering no block at all.
		return
	}
	item.Evidence = &out
}
