package backup

import (
	"strings"
	"testing"
)

func TestRestoredConversationActivityRequiresExplicitReenable(t *testing.T) {
	original := map[string]any{"conversation_id": "room", "issues": int64(1), "routines": int64(1), "issues_cursor": int64(32), "routines_cursor": int64(44)}
	row := safeConversationRestoreRow("workspace_conversation_activity", original, nil)
	for _, key := range []string{"issues", "routines", "issues_cursor", "routines_cursor"} {
		if row[key] != 0 {
			t.Errorf("%s not reset: %v", key, row[key])
		}
	}
	if original["issues"] != int64(1) || original["routines_cursor"] != int64(44) {
		t.Fatal("restore mutated source dump")
	}
	if row["conversation_id"] != "room" {
		t.Fatal("lost channel reference")
	}
}

func TestForkActivityLinksFollowWorkspaceButHumanTextDoesNot(t *testing.T) {
	content := "Routine run completed · [daily](/routines?slug=daily&workspace_id=old)"
	dump := &DBDump{Tables: map[string][]map[string]any{"workspace_conversation_messages": {
		{"client_id": "activity:4", "source_kind": "activity", "content": content},
		{"client_id": "activity:5", "source_kind": "", "content": content},
	}}}
	if err := remapConversationMetadata(dump, map[string]map[string]string{"workspaces": {"old": "new"}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dump.Tables["workspace_conversation_messages"][0]["content"].(string), "workspace_id=new") {
		t.Fatal("activity points at source workspace")
	}
	if dump.Tables["workspace_conversation_messages"][1]["content"] != content {
		t.Fatal("rewrote human text")
	}
}
