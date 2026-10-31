package api

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/mooring"
)

// PRD section 0 point 9: after a handoff the server refuses writes from the old
// lease generation on EVERY write path. This file is the guard: it enumerates
// the write routes from the real router's route table, so a new native Craft
// route that is not classified here fails the build instead of shipping unfenced.

// craftLeaseClass says how a native Craft write route relates to the lease.
type craftLeaseClass string

const (
	// Publishes or selects document bytes: must carry the holder's current
	// generation and be refused (409) with no lease or a stale one.
	craftFencedContent craftLeaseClass = "fenced-content"
	// Moves custody itself: the caller's generation must be the live one.
	craftFencedCustody craftLeaseClass = "fenced-custody"
	// Never changes document bytes and takes no generation by design; the test
	// proves it leaves versions untouched.
	craftNoDocumentWrite craftLeaseClass = "no-document-write"
)

var craftNativeWriteRoutes = map[string]craftLeaseClass{
	"POST /api/v1/pages/{slug}/craft/document/save":                 craftFencedContent,
	"POST /api/v1/pages/{slug}/craft/document/import":               craftFencedContent,
	"POST /api/v1/pages/{slug}/craft/document/restore":              craftFencedContent,
	"POST /api/v1/pages/{slug}/craft/document/share":                craftFencedContent,
	"POST /api/v1/pages/{slug}/craft/proposals/{proposalID}/accept": craftFencedContent,
	"POST /api/v1/internal/craft-native/task":                       craftFencedContent, // agent apply; see the agent test
	"POST /api/v1/pages/{slug}/craft/lease/renew":                   craftFencedCustody,
	"POST /api/v1/pages/{slug}/craft/lease/release":                 craftFencedCustody,
	"POST /api/v1/pages/{slug}/craft/lease/handoff":                 craftFencedCustody,
	"POST /api/v1/pages/{slug}/craft/lease":                         craftNoDocumentWrite, // acquire: refused while another holder is live
	"POST /api/v1/pages/{slug}/craft/lease/acquire":                 craftNoDocumentWrite,
	"POST /api/v1/pages/{slug}/craft/lease/request-handoff":         craftNoDocumentWrite,
	"POST /api/v1/pages/{slug}/craft/document/reconcile":            craftNoDocumentWrite, // read-only proof of an earlier commit
	"POST /api/v1/pages/{slug}/craft/initialize":                    craftNoDocumentWrite, // only on an empty Page; refused once native
	"POST /api/v1/pages/{slug}/craft/proposals/{proposalID}/reject": craftNoDocumentWrite,
	"POST /api/v1/pages/{slug}/craft/sessions":                      craftNoDocumentWrite,
	"POST /api/v1/pages/{slug}/craft/sessions/reauthorize":          craftNoDocumentWrite,
	"POST /api/v1/pages/{slug}/craft/tasks":                         craftNoDocumentWrite,
	"POST /api/v1/pages/{slug}/craft/tasks/cancel":                  craftNoDocumentWrite,
}

func craftNativeRealWriteRoutes(r *Router) []string {
	var out []string
	for path, methods := range r.mux.Routes() {
		if !strings.Contains(path, "/craft/") && !strings.Contains(path, "/craft-native/") {
			continue
		}
		for _, m := range methods {
			if m != "GET" && m != "HEAD" && m != "OPTIONS" {
				out = append(out, m+" "+path)
			}
		}
	}
	sort.Strings(out)
	return out
}

func TestCraftNativeEveryWriteRouteIsLeaseClassified(t *testing.T) {
	_, router, _, _, _, _ := craftNativeDataTCPFixture(t, true)
	real := craftNativeRealWriteRoutes(router)
	seen := map[string]bool{}
	for _, route := range real {
		seen[route] = true
		if _, ok := craftNativeWriteRoutes[route]; !ok {
			t.Errorf("write route %q is not lease-classified: add it to craftNativeWriteRoutes and prove it refuses a stale or missing generation", route)
		}
	}
	for route := range craftNativeWriteRoutes {
		if !seen[route] {
			t.Errorf("classified route %q is no longer registered; remove it", route)
		}
	}
	if len(real) < 15 {
		t.Fatalf("route enumeration looks broken: %v", real)
	}
}

type craftWriteState struct {
	Latest, Shared, Versions int
	Manifest, Lease, Props   string
}

func craftWriteSnapshot(t *testing.T, f *pagesTransferRig) craftWriteState {
	t.Helper()
	var s craftWriteState
	if err := f.db.QueryRow(`SELECT latest_version,shared_version FROM artifact_version_heads WHERE page_id='native-data-page'`).Scan(&s.Latest, &s.Shared); err != nil {
		t.Fatal(err)
	}
	f.db.QueryRow(`SELECT count(*), coalesce(group_concat(manifest_json,'|'),'') FROM artifact_versions WHERE page_id='native-data-page'`).Scan(&s.Versions, &s.Manifest)
	f.db.QueryRow(`SELECT coalesce(actor_id,'')||'/'||coalesce(session_id,'')||'/'||generation FROM mooring_edit_leases WHERE page_id='native-data-page'`).Scan(&s.Lease)
	f.db.QueryRow(`SELECT coalesce(group_concat(id||':'||state),'') FROM craft_native_proposals WHERE page_id='native-data-page'`).Scan(&s.Props)
	return s
}

func TestCraftNativeFencedWriteRoutesRefuseStaleOrMissingLease(t *testing.T) {
	f, router, call, owner, other, cfg := craftNativeDataTCPFixture(t, true)
	mustExec(t, f.db, `INSERT INTO craft_native_proposals(id,page_id,workspace_id,application_id,engine_pin,author_actor_id,request_id,base_version,summary,content_hash,content) VALUES('ncp_lease_guard','native-data-page',?,'wordcraft',?,'agent:fixture','run:r',1,'s','sha256:'||hex(zeroblob(32)),?)`, f.wsID, mooring.NewNativeStore(nil, cfg).EnginePin(), []byte(`{"v":77}`))
	at, aid := craftNativeDataTCPAdmission(t, call, owner)
	bt, bid := craftNativeDataTCPAdmission(t, call, other)
	pin := mooring.NewNativeStore(nil, cfg).EnginePin()

	type fenced struct {
		path string
		body func(session, gen, request string) any
	}
	snapshot := func(session, gen, _ string) any {
		return craftNativeDataTCPBody(f, cfg, session, gen, "1", "opaque:guard", []byte(`{"v":99}`))
	}
	routes := map[string]fenced{
		"POST /api/v1/pages/{slug}/craft/document/save":   {"document/save", snapshot},
		"POST /api/v1/pages/{slug}/craft/document/import": {"document/import", snapshot},
		"POST /api/v1/pages/{slug}/craft/document/restore": {"document/restore", func(s, g, _ string) any {
			b := craftNativeDataTCPBody(f, cfg, s, g, "1", "opaque:guard", nil)
			b["targetVersion"] = "1"
			return b
		}},
		"POST /api/v1/pages/{slug}/craft/document/share": {"document/share", func(s, g, _ string) any {
			return map[string]string{"workspaceId": f.wsID, "pageId": "native-data-page", "applicationId": "wordcraft", "enginePin": pin, "sessionId": s, "generation": g, "baseVersion": "1", "targetVersion": "1"}
		}},
		"POST /api/v1/pages/{slug}/craft/proposals/{proposalID}/accept": {"proposals/ncp_lease_guard/accept", func(s, g, _ string) any {
			return map[string]string{"sessionId": s, "generation": g}
		}},
		"POST /api/v1/pages/{slug}/craft/lease/renew":   {"lease/renew", func(_, g, _ string) any { return map[string]string{"generation": g} }},
		"POST /api/v1/pages/{slug}/craft/lease/release": {"lease/release", func(_, g, _ string) any { return map[string]string{"generation": g} }},
		"POST /api/v1/pages/{slug}/craft/lease/handoff": {"lease/handoff", func(_, g, r string) any { return map[string]string{"generation": g, "request_id": r} }},
	}
	// Every fenced route of the classification table (the agent route has its own test).
	for route, class := range craftNativeWriteRoutes {
		if class == craftNoDocumentWrite || route == "POST /api/v1/internal/craft-native/task" {
			continue
		}
		if _, ok := routes[route]; !ok {
			t.Errorf("fenced route %q has no stale-generation case", route)
		}
	}
	names := make([]string, 0, len(routes))
	for n := range routes {
		names = append(names, n)
	}
	sort.Strings(names)

	attempt := func(phase, who, token, session, gen, request string) {
		t.Helper()
		for _, name := range names {
			rt := routes[name]
			before := craftWriteSnapshot(t, f)
			status, raw := call("POST", rt.path, who, token, rt.body(session, gen, request))
			after := craftWriteSnapshot(t, f)
			// Handoff binds its request_id to one generation; a request that no
			// longer matches is 403 by spec (craft-lease-generation.md, status codes).
			want := status == 409 || (strings.HasSuffix(name, "/lease/handoff") && status == 403)
			if !want {
				t.Errorf("%s: %s with generation %q: got %d %s, want 409", phase, name, gen, status, raw)
			}
			if before != after {
				t.Errorf("%s: %s changed state despite refusal:\nbefore %+v\nafter  %+v", phase, name, before, after)
			}
		}
	}

	// 1. Nobody ever acquired a lease.
	attempt("never acquired", owner, at, aid, "1", "none")

	// 2. Owner holds generation 1, then hands off to the other session (generation 2).
	if s, raw := call("POST", "lease", owner, at, struct{}{}); s != 200 {
		t.Fatalf("acquire %d %s", s, raw)
	}
	s, raw := call("POST", "lease/request-handoff", other, bt, struct{}{})
	var req struct {
		ID string `json:"request_id"`
	}
	if s != 201 || json.Unmarshal(raw, &req) != nil {
		t.Fatalf("request handoff %d %s", s, raw)
	}
	if s, raw = call("POST", "lease/handoff", owner, at, map[string]string{"generation": "1", "request_id": req.ID}); s != 200 {
		t.Fatalf("handoff %d %s", s, raw)
	}
	// The former holder is a requester again so handoff has a live request to replay.
	s, raw = call("POST", "lease/request-handoff", owner, at, struct{}{})
	if s != 201 || json.Unmarshal(raw, &req) != nil {
		t.Fatalf("second request %d %s", s, raw)
	}
	attempt("old generation after handoff", owner, at, aid, "1", req.ID)
	attempt("holder with an older generation", other, bt, bid, "1", req.ID)
	attempt("holder with a future generation", other, bt, bid, "3", req.ID)

	// 3. Holder releases: the generation is kept but nobody holds the lease.
	if s, raw = call("POST", "lease/release", other, bt, map[string]string{"generation": "2"}); s != 200 {
		t.Fatalf("release %d %s", s, raw)
	}
	attempt("released lease", other, bt, bid, "2", req.ID)

	// Routes classified no-document-write never touch version bytes, whatever the lease says.
	for _, n := range []struct {
		path string
		body any
	}{
		{"lease/request-handoff", struct{}{}},
		{"lease", struct{}{}},
		{"sessions/reauthorize", struct{}{}},
		{"document/reconcile", craftNativeDataTCPBody(f, cfg, aid, "1", "1", "opaque:never-saved", []byte(`{"v":99}`))},
		{"initialize", map[string]any{"application_id": "wordcraft", "bytes": []byte(`{"v":99}`)}},
		{"proposals/ncp_lease_guard/reject", struct{}{}},
	} {
		before := craftWriteSnapshot(t, f)
		call("POST", n.path, owner, at, n.body)
		after := craftWriteSnapshot(t, f)
		if after.Latest != before.Latest || after.Versions != before.Versions || after.Manifest != before.Manifest || after.Shared != before.Shared {
			t.Errorf("%s changed document versions: %+v -> %+v", n.path, before, after)
		}
	}
	var head int
	if err := f.db.QueryRow(`SELECT latest_version FROM artifact_version_heads WHERE page_id='native-data-page'`).Scan(&head); err != nil || head != 1 {
		t.Fatalf("a refused write moved the head: %d %v", head, err)
	}
	_ = router
}

// The agent apply path takes its generation from the host-remembered task lease
// but must still be fenced by the stored lease on every call.
func TestCraftNativeAgentApplyRefusesStaleOrMissingLease(t *testing.T) {
	f, _, _, call, calls := nativeAgentToolFixture(t)
	head := func() int64 {
		var h int64
		if err := f.rig.db.QueryRow(`SELECT latest_version FROM artifact_version_heads WHERE page_id='native-agent-page'`).Scan(&h); err != nil {
			t.Fatal(err)
		}
		return h
	}
	apply := func(gen, id string) (int, string) {
		w := call(fmt.Sprintf(`{"operation":"apply","generation":%q,"base_version":"1","request_id":%q,"commands":[{"command":"text.insert","params":{"text":"x"}}]}`, gen, id))
		return w.Code, w.Body.String()
	}
	check := func(label string, code int, body string) {
		t.Helper()
		if code != 409 || *calls != 0 || head() != 1 {
			t.Fatalf("%s: status %d %s engine calls %d head %d", label, code, body, *calls, head())
		}
	}
	code, body := apply("1", "no-lease")
	check("no lease acquired", code, body)

	w := call(`{"operation":"acquire"}`)
	var lease struct {
		Generation string `json:"generation"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &lease) != nil {
		t.Fatal("agent acquire", w.Code, w.Body)
	}
	code, body = apply("999", "wrong-generation")
	check("generation the task never held", code, body)

	// Custody moves to someone else underneath the agent (handoff bumps the generation).
	mustExec(t, f.rig.db, `UPDATE mooring_edit_leases SET actor_id='user:other',session_id='other-session',generation=generation+1 WHERE page_id='native-agent-page'`)
	code, body = apply(lease.Generation, "after-handoff")
	check("old generation after handoff", code, body)

	// Revoked (task stopped): same generation, no holder.
	mustExec(t, f.rig.db, `UPDATE mooring_edit_leases SET actor_id='',session_id='',expires_ns=0 WHERE page_id='native-agent-page'`)
	code, body = apply(lease.Generation, "after-revoke")
	check("revoked lease", code, body)
	_ = context.Background
}
