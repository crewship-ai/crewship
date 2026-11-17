package api

import (
 "net/http"
 "testing"
)

func TestCrewCodexOuterSandboxRequiresWorkspaceAdministration(t *testing.T) {
 h,db,user,ws:=covCruNewCrew(t)
 crew:=seedCrewRow(t,db,"outer-crew",ws,"Outer fixture","outer-fixture")
 // Crew management must not confer authority to weaken the nested sandbox.
 rr:=covCruDoUpdate(h,crew,user,ws,"MANAGER",`{"allow_codex_outer_sandbox":true}`)
 if rr.Code!=http.StatusForbidden { t.Fatalf("manager sandbox opt-in: got %d want403: %s",rr.Code,rr.Body.String()) }
}

func TestCrewCodexOuterSandboxDefaultsOffAndPersistsExplicitConsent(t *testing.T) {
 h,db,user,ws:=covCruNewCrew(t)
 crew:=seedCrewRow(t,db,"outer-crew",ws,"Outer fixture","outer-fixture")
 var enabled bool
 if err:=db.QueryRow(`SELECT allow_codex_outer_sandbox FROM crews WHERE id=?`,crew).Scan(&enabled);err!=nil {t.Fatal(err)}
 if enabled {t.Fatal("fresh crew enabled without consent")}
 for _,body:=range []string{`{"allow_codex_outer_sandbox":true}`,`{"allow_codex_outer_sandbox":false}`} {
  rr:=covCruDoUpdate(h,crew,user,ws,"OWNER",body)
  if rr.Code!=http.StatusOK {t.Fatalf("owner update: %d %s",rr.Code,rr.Body.String())}
  if err:=db.QueryRow(`SELECT allow_codex_outer_sandbox FROM crews WHERE id=?`,crew).Scan(&enabled);err!=nil {t.Fatal(err)}
  if enabled!=(body==`{"allow_codex_outer_sandbox":true}`) {t.Fatal("explicit consent did not persist")}
 }
}
