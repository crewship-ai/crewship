package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/crewship-ai/crewship/internal/documents"
)

func validDocumentStructureTarget(target documentVerifiedTarget) bool {
	scope := target.Structure
	if scope == nil || scope.Version != 1 || !scope.AllowInsertAfter || len(scope.Blocks) < 1 || len(scope.Blocks) > 256 || target.Schema != 3 || target.Text != "" || target.Quote != "" || target.From != 0 || target.To != 0 || len(target.Ancestors) != 0 {
		return false
	}
	seen := map[string]bool{}
	for _, block := range scope.Blocks {
		if !documentRichID.MatchString(block.ID) || len(block.Hash) != 64 || seen[block.ID] {
			return false
		}
		seen[block.ID] = true
	}
	for _, boundary := range []*DocumentBlockHash{scope.Before, scope.After} {
		if boundary != nil {
			if !documentRichID.MatchString(boundary.ID) || len(boundary.Hash) != 64 || seen[boundary.ID] {
				return false
			}
			seen[boundary.ID] = true
		}
	}
	return target.ID == scope.Blocks[0].ID && target.Hash == scope.Blocks[0].Hash
}

// This proof is checked independently of the engine inside the durable writer
// transaction, against the original immutable human request and stored hashes.
func documentStructuralGuard(ctx context.Context, tx *sql.Tx, workspace, page, request string, target documentVerifiedTarget, semantic *documentSemanticPatch, hashes map[string]string, projection json.RawMessage, state documents.State) error {
	if state.Schema != 3 || !validDocumentStructureTarget(target) || semantic == nil || semantic.Structure == nil || semantic.Target != "" || semantic.Text != "" || semantic.Edit != nil || semantic.Rich != nil {
		return documents.ErrConflict
	}
	proof := semantic.Structure
	scope := target.Structure
	if proof.RequestID != request || len(request) > 64 || len(proof.Operations) < 1 || len(proof.Operations) > 256 || len(hashes) != len(scope.Blocks) {
		return documents.ErrConflict
	}
	rawOps, err := json.Marshal(proof.Operations)
	if err != nil || len(rawOps) > 256<<10 {
		return documents.ErrConflict
	}
	before, err := documentRichTree(proof.Before)
	if err != nil {
		return documents.ErrConflict
	}
	after, err := documentRichTree(proof.After)
	if err != nil || !documentRichProjectionMatches(before, state.Projection) || !documentRichProjectionMatches(after, projection) {
		return documents.ErrConflict
	}
	roots, _ := before["content"].([]any)
	nodeID := func(value any) string {
		node, _ := value.(map[string]any)
		attrs, _ := node["attrs"].(map[string]any)
		id, _ := attrs["id"].(string)
		return id
	}
	find := func(id string) int {
		for i, node := range roots {
			if nodeID(node) == id {
				return i
			}
		}
		return -1
	}
	start := find(scope.Blocks[0].ID)
	if start < 0 || start+len(scope.Blocks) > len(roots) {
		return documents.ErrConflict
	}
	allowed := map[string]bool{}
	usedIDs := map[string]bool{}
	var remember func(map[string]any)
	remember = func(node map[string]any) {
		if id := nodeID(node); id != "" {
			usedIDs[id] = true
		}
		children, _ := node["content"].([]any)
		for _, child := range children {
			remember(child.(map[string]any))
		}
	}
	remember(before)
	for i, block := range scope.Blocks {
		node := roots[start+i].(map[string]any)
		if nodeID(node) != block.ID || documentRichHash(node) != block.Hash || hashes[block.ID] != block.Hash {
			return documents.ErrConflict
		}
		allowed[block.ID] = true
	}
	boundary := func(index int, want *DocumentBlockHash) bool {
		if index < 0 || index >= len(roots) {
			return want == nil
		}
		return want != nil && nodeID(roots[index]) == want.ID && documentRichHash(roots[index].(map[string]any)) == want.Hash
	}
	if !boundary(start-1, scope.Before) || !boundary(start+len(scope.Blocks), scope.After) {
		return documents.ErrConflict
	}
	moved := map[string]bool{}
	var collect func(map[string]any)
	collect = func(node map[string]any) {
		if id := nodeID(node); id != "" {
			moved[id] = true
		}
		children, _ := node["content"].([]any)
		for _, child := range children {
			collect(child.(map[string]any))
		}
	}
	for index, op := range proof.Operations {
		at := find(op.ID)
		switch op.Op {
		case "insert_after":
			anchor := find(op.AfterID)
			if op.ID != "" || op.BeforeID != "" || op.IfHash != "" || anchor < 0 || !allowed[op.AfterID] || op.AnchorHash != documentRichHash(roots[anchor].(map[string]any)) {
				return documents.ErrConflict
			}
			var node map[string]any
			decoder := json.NewDecoder(bytes.NewReader(op.Node))
			decoder.UseNumber()
			if decoder.Decode(&node) != nil || nodeID(node) != fmt.Sprintf("%s-new-%d", request, index) || usedIDs[nodeID(node)] {
				return documents.ErrConflict
			}
			typ, _ := node["type"].(string)
			if typ != "paragraph" && typ != "heading" && typ != "codeBlock" {
				return documents.ErrConflict
			}
			children, _ := node["content"].([]any)
			for _, value := range children {
				child, ok := value.(map[string]any)
				if !ok || child["type"] != "text" || child["marks"] != nil {
					return documents.ErrConflict
				}
			}
			envelope, _ := json.Marshal(map[string]any{"type": "doc", "content": []any{node}})
			if _, err = documentRichTree(envelope); err != nil {
				return documents.ErrConflict
			}
			allowed[nodeID(node)] = true
			usedIDs[nodeID(node)] = true
			roots = append(roots, nil)
			copy(roots[anchor+2:], roots[anchor+1:])
			roots[anchor+1] = node
		case "delete":
			if at < 0 || !allowed[op.ID] || op.AfterID != "" || op.BeforeID != "" || op.AnchorHash != "" || len(op.Node) != 0 || op.IfHash != documentRichHash(roots[at].(map[string]any)) {
				return documents.ErrConflict
			}
			roots = append(roots[:at], roots[at+1:]...)
		case "move_before":
			anchor := find(op.BeforeID)
			if at < 0 || anchor < 0 || at == anchor || !allowed[op.ID] || !allowed[op.BeforeID] || op.AfterID != "" || len(op.Node) != 0 || op.IfHash != documentRichHash(roots[at].(map[string]any)) || op.AnchorHash != documentRichHash(roots[anchor].(map[string]any)) {
				return documents.ErrConflict
			}
			node := roots[at]
			collect(node.(map[string]any))
			roots = append(roots[:at], roots[at+1:]...)
			anchor = find(op.BeforeID)
			roots = append(roots, nil)
			copy(roots[anchor+1:], roots[anchor:])
			roots[anchor] = node
		default:
			return documents.ErrConflict
		}
		if len(roots) == 0 {
			return documents.ErrConflict
		}
		before["content"] = roots
	}
	documentRichNormalize(before)
	documentRichNormalize(after)
	if !reflect.DeepEqual(before, after) {
		return documents.ErrConflict
	}
	if len(moved) > 0 {
		rows, err := tx.QueryContext(ctx, `SELECT anchor_json FROM document_comments WHERE workspace_id=? AND page_id=? LIMIT 1001`, workspace, page)
		if err != nil {
			return err
		}
		defer rows.Close()
		count := 0
		for rows.Next() {
			count++
			var raw []byte
			var anchor struct {
				Block string `json:"block"`
			}
			if rows.Scan(&raw) != nil || json.Unmarshal(raw, &anchor) != nil || anchor.Block == "" || moved[anchor.Block] || count > 1000 {
				return documents.ErrConflict
			}
		}
		if err = rows.Err(); err != nil {
			return err
		}
	}
	return nil
}

func validDocumentStructureSelection(selection *DocumentStructureSelection) bool {
	if selection == nil || !selection.AllowInsertAfter || len(selection.BlockIDs) == 0 || len(selection.BlockIDs) > 256 {
		return false
	}
	seen := map[string]bool{}
	for _, id := range selection.BlockIDs {
		if !documentRichID.MatchString(id) || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func documentStructureScopeMatches(root map[string]any, scope *documentStructureScope) bool {
	if scope == nil || len(scope.Blocks) == 0 {
		return false
	}
	roots, _ := root["content"].([]any)
	id := func(value any) string {
		node, _ := value.(map[string]any)
		attrs, _ := node["attrs"].(map[string]any)
		valueID, _ := attrs["id"].(string)
		return valueID
	}
	start := -1
	for i, node := range roots {
		if id(node) == scope.Blocks[0].ID {
			start = i
			break
		}
	}
	if start < 0 || start+len(scope.Blocks) > len(roots) {
		return false
	}
	for i, block := range scope.Blocks {
		if id(roots[start+i]) != block.ID || documentRichHash(roots[start+i].(map[string]any)) != block.Hash {
			return false
		}
	}
	boundary := func(index int, want *DocumentBlockHash) bool {
		if index < 0 || index >= len(roots) {
			return want == nil
		}
		return want != nil && id(roots[index]) == want.ID && documentRichHash(roots[index].(map[string]any)) == want.Hash
	}
	return boundary(start-1, scope.Before) && boundary(start+len(scope.Blocks), scope.After)
}
