package backup

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
)

// Compare JSON messages rather than serialization choices such as key order.
func hashTranscriptLine(dst hash.Hash, line []byte) error {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(line))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, _ = dst.Write(normalized)
	_, _ = dst.Write([]byte{'\n'})
	return nil
}

func transcriptDigest(src io.Reader) ([]byte, error) {
	digest := sha256.New()
	scanner := bufio.NewScanner(src)
	scanner.Buffer(make([]byte, 64<<10), 64<<20)
	for scanner.Scan() {
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		if !json.Valid(scanner.Bytes()) {
			return nil, fmt.Errorf("backup: invalid existing transcript message")
		}
		if err := hashTranscriptLine(digest, scanner.Bytes()); err != nil {
			return nil, err
		}
	}
	return digest.Sum(nil), scanner.Err()
}

func remapTranscriptPageContext(message map[string]json.RawMessage, remap map[string]map[string]string) error {
	if len(message["metadata"]) == 0 || bytes.Equal(message["metadata"], []byte("null")) {
		return nil
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(message["metadata"], &metadata); err != nil {
		return err
	}
	if len(metadata["page_context"]) == 0 || bytes.Equal(metadata["page_context"], []byte("null")) {
		return nil
	}
	var page map[string]json.RawMessage
	if err := json.Unmarshal(metadata["page_context"], &page); err != nil {
		return err
	}
	changed := false
	for field, table := range map[string]string{"workspace_id": "workspaces", "page_id": "pages"} {
		var id string
		_ = json.Unmarshal(page[field], &id)
		if mapped := remap[table][id]; mapped != "" && mapped != id {
			page[field], _ = json.Marshal(mapped)
			changed = true
		}
	}
	if changed {
		metadata["page_context"], _ = json.Marshal(page)
		message["metadata"], _ = json.Marshal(metadata)
	}
	return nil
}

func reconcileTranscriptUsers(dump *DBDump, reconciled map[string]string) {
	if dump.chatTranscriptRemap == nil {
		dump.chatTranscriptRemap = map[string]map[string]string{}
	}
	original := dump.chatTranscriptRemap["users"]
	combined := make(map[string]string, len(original)+len(reconciled))
	for source, target := range original {
		if mapped := reconciled[target]; mapped != "" {
			target = mapped
		}
		combined[source] = target
	}
	for source, target := range reconciled {
		combined[source] = target
	}
	dump.chatTranscriptRemap["users"] = combined
}

type transcriptChatReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// Bundle metadata is insufficient after INSERT OR IGNORE. Check actual rows
// in the target transaction, including collisions with a different workspace.
func checkedTranscriptChats(ctx context.Context, reader transcriptChatReader, dump *DBDump, requireAll bool) (map[string]bool, error) {
	found := map[string]bool{}
	if dump == nil || len(dump.AgentChatTranscripts) == 0 {
		return found, nil
	}
	workspace := firstWorkspaceID(dump)
	allowed := map[string]bool{}
	for _, row := range dump.Tables["chats"] {
		id, _ := row["id"].(string)
		rowWorkspace, _ := row["workspace_id"].(string)
		allowed[id] = workspace != "" && rowWorkspace == workspace
	}
	for _, id := range dump.AgentChatTranscripts {
		if !allowed[id] {
			return nil, fmt.Errorf("backup: transcript chat %s is outside the restored workspace", id)
		}
		var actual string
		err := reader.QueryRowContext(ctx, `SELECT workspace_id FROM chats WHERE id=?`, id).Scan(&actual)
		if errors.Is(err, sql.ErrNoRows) && !requireAll {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("backup: verify transcript chat %s: %w", id, err)
		}
		if actual != workspace {
			return nil, fmt.Errorf("backup: transcript chat %s collides with another workspace", id)
		}
		found[id] = true
	}
	return found, nil
}
