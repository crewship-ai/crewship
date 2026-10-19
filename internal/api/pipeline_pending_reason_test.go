package api

import (
 "encoding/json"
 "net/http/httptest"
 "testing"
 "time"

 "github.com/crewship-ai/crewship/internal/pipeline"
)

func TestPendingReceiptReasonCodeWireAndWorkspaceBoundary(t *testing.T) {
 h, db, user, ws := scheduleHandlerRig(t)
 seedPipelineRow(t, db, ws, "reason-recipe", "reason-recipe")
 store := pipeline.NewPendingRunStore(db)
 if _, _, err := store.Enqueue(t.Context(), pipeline.PendingRun{ID:"reason", WorkspaceID:ws, PipelineID:"reason-recipe", PipelineSlug:"reason-recipe", FireAt:time.Now().Add(-time.Minute)}); err != nil { t.Fatal(err) }
 for _, code := range []string{"routine_capacity", "recovery_unknown", "", "future_server_code"} {
  if _,err:=db.ExecContext(t.Context(),`UPDATE pending_runs SET reason_code=? WHERE id='reason'`,code);err!=nil {t.Fatal(err)}
  req:=withWorkspaceUser(httptest.NewRequest("GET","/pending/reason",nil),user,ws,"VIEWER"); req.SetPathValue("pendingId","reason")
  rr:=httptest.NewRecorder();h.GetPendingRun(rr,req)
  var body map[string]any
  if err:=json.Unmarshal(rr.Body.Bytes(),&body);err!=nil {t.Fatal(err)}
  want:=code;if want=="" {want="unknown"}
  if body["reason_code"]!=want {t.Fatalf("reason=%q response=%s",want,rr.Body)}
 }
 req:=withWorkspaceUser(httptest.NewRequest("GET","/pending/reason",nil),user,"another-workspace","OWNER"); req.SetPathValue("pendingId","reason")
 rr:=httptest.NewRecorder();h.GetPendingRun(rr,req)
 if rr.Code!=404 {t.Fatalf("cross workspace disclosure: %d %s",rr.Code,rr.Body)}
}
