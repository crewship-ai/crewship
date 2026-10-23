package api

import (
 "net/http"
 "net/http/httptest"
 "os"
 "os/exec"
 "strconv"
 "strings"
 "sync/atomic"
 "testing"
 "time"

 "golang.org/x/crypto/bcrypt"
)

// Run in a child so replacing the existing lazy-dummy seam cannot race
// another test's Router warmup. This exercises the real registered handler;
// the barrier controls the work boundary, not bcrypt's timing.
func TestPublicUnlock_AdmissionBeforeDummyWork(t *testing.T) {
 if os.Getenv("CREWSHIP_UNLOCK_ADMISSION_CHILD") != "1" {
  exe,err:=os.Executable();if err!=nil {t.Fatal(err)}
  cmd:=exec.Command(exe,"-test.run=^TestPublicUnlock_AdmissionBeforeDummyWork$","-test.timeout=2m","-test.v")
  for _,v:=range os.Environ() {if !strings.HasPrefix(v,"GOMAXPROCS=")&&!strings.HasPrefix(v,"CREWSHIP_UNLOCK_ADMISSION_CHILD=") {cmd.Env=append(cmd.Env,v)}}
  cmd.Env=append(cmd.Env,"GOMAXPROCS=2","CREWSHIP_UNLOCK_ADMISSION_CHILD=1")
  if out,err:=cmd.CombinedOutput();err!=nil {t.Fatalf("unlock boundary child: %v\n%s",err,out)} else {t.Log(string(out))}
  return
 }
 h,_,_,_,_:=newPagesPublicFixture(t,"unlock-admission-boundary")
 hash,err:=bcrypt.GenerateFromPassword([]byte("owned-synthetic-password"),ProductionBcryptCost);if err!=nil {t.Fatal(err)}
 entered:=make(chan struct{});release:=make(chan struct{});done:=make(chan struct{})
 original:=dummyBcryptHash
 var calls atomic.Int32
 dummyBcryptHash=func() string {if calls.Add(1)==1 {close(entered);<-release};return string(hash)}
 defer func(){close(release);<-done;dummyBcryptHash=original}()
 mux:=http.NewServeMux();mux.HandleFunc("POST /api/v1/public/pages/{token}/unlock",h.Unlock)
 request:=func(token string)*httptest.ResponseRecorder {rr:=httptest.NewRecorder();req:=httptest.NewRequest("POST","/api/v1/public/pages/"+token+"/unlock",strings.NewReader(`{"password":"wrong"}`));mux.ServeHTTP(rr,req);return rr}
 go func(){defer close(done);request("first-owned-unknown-token")}()
 select {case <-entered:case <-time.After(30*time.Second):t.Fatal("first request never reached dummy boundary")}
 rr:=request("second-distinct-owned-unknown-token")
 if rr.Code!=http.StatusTooManyRequests {t.Errorf("busy public unlock returned %d, want429 before dummy work: %s",rr.Code,rr.Body.String())}
 retry,err:=strconv.Atoi(rr.Header().Get("Retry-After"));if err!=nil||retry<1 {t.Errorf("busy response missing positive Retry-After: %q",rr.Header().Get("Retry-After"))}
 if got:=calls.Load();got!=1 {t.Errorf("overload entered dummy lookup %d times, want1",got)}
}
