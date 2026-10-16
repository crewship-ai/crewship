package backup

import (
	"fmt"

	"github.com/crewship-ai/crewship/internal/slides"
)

type selectedForkBinding struct {
	row             map[string]any
	oldKey          slides.Key
	binding         slides.StoredSelectedBinding
	verified        slides.VerifiedTextSelection
	resultRevision  int64
	baseValidated   bool
	resultValidated bool
}
type selectedForkTracker struct {
	requests []*selectedForkBinding
	byPage   map[string][]*selectedForkBinding
	hashes   map[slides.Key]map[int64]string
}

func selectedForkBindings(dump *DBDump) (*selectedForkTracker, error) {
	tracker := &selectedForkTracker{byPage: map[string][]*selectedForkBinding{}, hashes: map[slides.Key]map[int64]string{}}
	seen := map[string]bool{}
	for _, row := range dump.Tables["slides_requests"] {
		_, binding, err := selectedBindingRow(row)
		if err != nil {
			return nil, err
		}
		// The workspace bundle keeps its original scope anchor during RemapIDs;
		// generic FK rewriting may already have changed the row's workspace.
		// Decode the original Page directly from its exact stored input below.
		oldPage, err := slides.SelectedBindingPageID(binding)
		if err != nil {
			return nil, err
		}
		oldKey := slides.Key{WorkspaceID: dump.WorkspaceID, PageID: oldPage}
		_, verified, err := slides.DecodeStoredSelectedBinding(oldKey, binding)
		if err != nil {
			return nil, fmt.Errorf("backup: invalid source selected binding: %w", err)
		}
		id, ok := row["id"].(string)
		if !ok || id == "" || seen[id] {
			return nil, fmt.Errorf("backup: duplicate or missing selected request identity")
		}
		seen[id] = true
		item := &selectedForkBinding{row: row, oldKey: oldKey, binding: binding, verified: verified}
		if row["result_revision"] != nil {
			item.resultRevision, err = selectedRestoreInt(row["result_revision"])
			if err != nil || item.resultRevision != binding.BaseRevision+1 {
				return nil, fmt.Errorf("backup: invalid source selected result revision")
			}
		}
		tracker.requests = append(tracker.requests, item)
		tracker.byPage[oldKey.PageID] = append(tracker.byPage[oldKey.PageID], item)
	}
	return tracker, nil
}

func (t *selectedForkTracker) validateSourceRevision(row map[string]any, deck slides.Deck, hash string) error {
	if len(t.byPage[deck.DeckID]) == 0 {
		return nil
	}
	revision, err := selectedRestoreInt(row["rev"])
	if err != nil {
		return err
	}
	state := slides.State{Revision: revision, SchemaVersion: deck.SchemaVersion, ContentHash: hash, Deck: deck}
	for _, item := range t.byPage[deck.DeckID] {
		if revision == item.binding.BaseRevision {
			if err := slides.ValidateVerifiedTextSelection(state, item.oldKey.PageID, revision, item.verified); err != nil {
				return fmt.Errorf("backup: selected original source snapshot mismatch: %w", err)
			}
			item.baseValidated = true
		}
		if revision == item.resultRevision {
			parent, err := selectedRestoreInt(row["parent_rev"])
			run, _ := item.row["original_run_id"].(string)
			origin, _ := item.row["origin_request_id"].(string)
			agent, _ := item.row["agent_actor"].(string)
			if err != nil || parent != item.binding.BaseRevision || row["content_hash"] != item.row["result_hash"] || row["mutation_id"] != origin+"-patch" || row["actor"] != agent+":"+run || run == "" || row["kind"] != "patch" {
				return fmt.Errorf("backup: invalid source request-owned result")
			}
			item.resultValidated = true
		}
	}
	return nil
}

func (t *selectedForkTracker) recordRewrittenRevision(row map[string]any) error {
	if len(t.requests) == 0 {
		return nil
	}
	workspace, _ := row["workspace_id"].(string)
	page, _ := row["page_id"].(string)
	hash, _ := row["content_hash"].(string)
	revision, err := selectedRestoreInt(row["rev"])
	if err != nil {
		return err
	}
	key := slides.Key{WorkspaceID: workspace, PageID: page}
	if t.hashes[key] == nil {
		t.hashes[key] = map[int64]string{}
	}
	t.hashes[key][revision] = hash
	return nil
}

func (t *selectedForkTracker) remapRows(idMap map[string]map[string]string) error {
	for _, item := range t.requests {
		if !item.baseValidated || (item.resultRevision != 0 && !item.resultValidated) {
			return fmt.Errorf("backup: selected pinned source revision missing")
		}
		workspace, _ := item.row["workspace_id"].(string)
		page, _ := item.row["page_id"].(string)
		if page == "" || page != idMap["pages"][item.oldKey.PageID] {
			return fmt.Errorf("backup: selected fork Page identity mismatch")
		}
		key := slides.Key{WorkspaceID: workspace, PageID: page}
		baseHash := t.hashes[key][item.binding.BaseRevision]
		remapped, err := slides.RemapStoredSelectedBinding(item.oldKey, key, item.binding, baseHash)
		if err != nil {
			return err
		}
		item.row["base_content_hash"], item.row["input_json"], item.row["verified_json"], item.row["fingerprint"] = remapped.BaseContentHash, remapped.InputJSON, remapped.VerifiedJSON, remapped.Fingerprint
		if item.resultRevision != 0 {
			resultHash := t.hashes[key][item.resultRevision]
			if resultHash == "" {
				return fmt.Errorf("backup: rewritten selected result missing")
			}
			item.row["result_hash"] = resultHash
		}
	}
	return nil
}
