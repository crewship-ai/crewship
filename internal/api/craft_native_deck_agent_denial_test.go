package api

import (
	"reflect"
	"testing"

	"github.com/crewship-ai/crewship/internal/dispatch"
)

// Direct task admission with real SQL/current-human authority and qualified
// profile metadata. Readiness pointers are synthetic: no provider/CLI execution
// or genuine agent acceptance is claimed by this regression.
func TestCraftNativeDeckTaskDeniedBeforeWorkDespiteReadyQualifiedManager(t *testing.T) {
	for _, application := range []string{"wordcraft", "gridcraft", "deckcraft"} {
		t.Run(application, func(t *testing.T) {
			f, m, ctx := nativeAgentAdmissionFixture(t)
			profiles := map[string]CraftNativeDocumentProfile{}
			original := craftNativeProfileFixture(t) // Pinned synthetic executable, never invoked.
			for _, app := range []string{"wordcraft", "gridcraft", "deckcraft"} {
				p := original
				p.Application = app
				profiles[app] = p
			}
			qualified, err := qualifyCraftNativeProfiles(profiles)
			if err != nil {
				t.Fatal(err)
			}
			m.documents.applications = qualified
			m.config.Profiles = profiles
			m.runtime = &craftNativeAgentRuntime{}
			m.dispatcher = &dispatch.Dispatcher{}
			m.human.agents = m
			if !m.runtimeReady() || qualified[application].Store.Validate == nil {
				t.Fatal("qualified/ready admission fixture is not calibrated")
			}
			page := "native-agent-qualified-" + application
			f.rig.seedPage(t, page, page, "Qualified native admission", f.a.HumanUserID, "")
			p := profiles[application]
			mustExec(t, f.rig.db, `INSERT INTO craft_native_documents(page_id,workspace_id,application_id,engine_revision,engine_sha256,editor_bundle) VALUES(?,?,?,?,?,?)`, page, f.rig.wsID, application, p.EngineRevision, p.CLISHA256, p.EditorBundle)
			counts := func() []int {
				t.Helper()
				result := make([]int, 0, 5)
				for _, table := range []string{"work_items", "work_attempts", "artifact_project_message_intents", "mooring_edit_leases"} {
					var count int
					if err := f.rig.db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil {
						t.Fatal(err)
					}
					result = append(result, count)
				}
				return append(result, len(m.entries))
			}
			before := counts()
			receipt, err := m.produceTask(ctx, "@"+page, craftNativeAgentTaskRequest{ChatID: f.a.ChatID, MessageID: f.a.MessageID, WholeDocument: true})
			after := counts()
			if application == "deckcraft" {
				if err == nil || receipt.WorkID != "" {
					t.Errorf("unsupported Deck admitted work: receipt=%+v err=%v", receipt, err)
				}
				if !reflect.DeepEqual(before, after) {
					t.Errorf("denial changed work/attempt/intent/lease/grant state: before=%v after=%v", before, after)
				}
			} else {
				if err != nil || receipt.WorkID == "" || after[0] != before[0]+1 || after[4] != before[4]+1 {
					t.Fatalf("supported %s task calibration failed: receipt=%+v err=%v before=%v after=%v", application, receipt, err, before, after)
				}
			}
		})
	}
}
