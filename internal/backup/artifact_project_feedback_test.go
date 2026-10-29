package backup

import "testing"

func TestArtifactProjectBackupFeedbackAndAuthorityClassification(t *testing.T) {
	for _, table := range []string{"artifact_project_comments", "artifact_project_comment_tasks", "artifact_project_comment_replies", "artifact_project_origins"} {
		if intent, ok := BackupTableIntent[table]; !ok || intent != IntentInclude {
			t.Errorf("durable feedback omitted: %s", table)
		}
		found := false
		for _, name := range BackupTables {
			if name == table {
				found = true
			}
		}
		if !found {
			t.Errorf("feedback not dumped: %s", table)
		}
	}
	for _, table := range []string{"artifact_project_run_consents", "artifact_project_message_intents"} {
		if intent, ok := BackupTableIntent[table]; !ok || intent != IntentExcludeRuntime {
			t.Errorf("live authority must be excluded: %s", table)
		}
		reset := false
		for _, entry := range runtimeResets {
			if entry.table == table {
				reset = true
			}
		}
		if !reset {
			t.Errorf("live authority survives instance recovery: %s", table)
		}
	}
}
