package api

// cross_workspace_fence_matrix_test.go — the workspace fence, attacked rather
// than read.
//
// Why this file exists. The 2026-07-30 test-suite audit (§3) removed the
// membership fence from `RequireWorkspace` — mutant M4, a failed membership
// lookup falling through as MEMBER instead of 403 — and found it killed by
// exactly one test, `TestRequireWorkspace_NotMember`. Four of four mutants died,
// so the authorization core is genuinely pinned; but tenant isolation, the one
// invariant a multi-tenant product cannot get wrong even once, was resting on a
// single assertion. One refactor that rewrites that test leaves the fence
// unattended and nothing turns red. The failure is silent and total: the first
// person to notice is a customer reading another customer's data.
//
// What it does differently from TestCrossWorkspaceIDOR_GetPatchDelete
// (cross_workspace_idor_test.go). That test injects the workspace context
// directly into the request via withWorkspaceUser and calls handler methods.
// That proves the query-level `AND workspace_id = ?` scoping, but it cannot
// prove the ingress: it never runs RequireWorkspace, never resolves a real
// token, and never sees the router's role/scope chain. Everything here goes
// through `Router.ServeHTTP` with a real CLI token, so a hole anywhere in
// middleware → route registration → handler is reachable.
//
// The three attacks, per resource kind:
//
//	direct   — WS-A's OWNER, authorized for WS-A, names a WS-B object by id.
//	           Every canonical GET / PATCH / PUT / DELETE / action must be
//	           non-2xx, and a rejected DELETE must leave the victim row intact.
//	graft    — for nested paths, WS-A's own parent id with WS-B's leaf id
//	           (/crews/{mine}/missions/{theirs}). This is the #1471 shape at the
//	           path level: not "fetch a foreign row" but "hang a foreign row off
//	           one I own".
//	no-leak  — collections. A 200 is legitimate here (an empty list is the
//	           correct answer), so the assertion is on content: WS-B's seeded
//	           marker string must not appear in a body served to WS-A.
//
// Adding a resource type is adding a row to fenceKinds(). That is the point —
// the next resource inherits this coverage instead of depending on someone
// remembering to write it.
//
// Companion files: cross_workspace_reference_test.go covers foreign keys
// arriving in the request *body* (the actual #1471/#1481 class), and
// cross_workspace_path_query_test.go covers routes that carry {workspaceId} in
// the path while RequireWorkspace reads the query.

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// fenceNoopRunner satisfies pipeline.AgentRunner so the schedule/run route gets
// past its "runner not wired" 503 and actually reaches the workspace check at
// pipeline_schedules.go RunSchedule. It returns an error rather than a result:
// if the fence ever fails open, the probe must not go on to really execute
// another tenant's routine.
type fenceNoopRunner struct{}

func (fenceNoopRunner) RunStep(context.Context, pipeline.AgentStepRequest) (pipeline.AgentStepResult, error) {
	return pipeline.AgentStepResult{}, errors.New("fence test runner: refusing to execute")
}

// ---------------------------------------------------------------------------
// tenants
// ---------------------------------------------------------------------------

// fenceTenant is one fully-formed isolation boundary: a user, a workspace, an
// OWNER membership, a real CLI token, and one seeded row per resource kind.
// OWNER is deliberate — it is the strongest role a tenant can field, so nothing
// here can pass merely because the caller was under-privileged inside its own
// workspace.
type fenceTenant struct {
	tag    string
	wsID   string
	userID string
	token  string
	// ids maps a template placeholder ({crewId}, {missionId}, …) to the id of
	// the row this tenant owns. Placeholder names are local to this file; they
	// do not have to match the route's own path-parameter names.
	ids map[string]string
}

// marker returns the unique string seeded into this tenant's rows. If it shows
// up in a body served to the other tenant, content crossed the fence.
func (ten *fenceTenant) marker(kind string) string {
	return "FENCEMARK-" + strings.ToUpper(ten.tag) + "-" + strings.ToUpper(kind)
}

func fenceSeedTenant(t *testing.T, db *sql.DB, tag string) *fenceTenant {
	t.Helper()
	ten := &fenceTenant{
		tag:    tag,
		wsID:   "ws-fence-" + tag,
		userID: "u-fence-" + tag,
		ids:    map[string]string{},
	}
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, ?, ?)`,
		ten.userID, ten.userID+"@example.com", "Fence "+tag); err != nil {
		t.Fatalf("seed user %s: %v", ten.userID, err)
	}
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, ?, ?)`,
		ten.wsID, "Fence "+tag, "fence-"+tag); err != nil {
		t.Fatalf("seed workspace %s: %v", ten.wsID, err)
	}
	if _, err := db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES (?, ?, ?, 'OWNER')`,
		"wm-fence-"+tag, ten.wsID, ten.userID); err != nil {
		t.Fatalf("seed membership %s: %v", ten.wsID, err)
	}
	ten.token = mintTokenFor(t, db, ten.userID, "fence"+tag+strings.Repeat("0", 24))

	for _, k := range fenceKinds() {
		k.seed(t, db, ten)
	}
	return ten
}

// ---------------------------------------------------------------------------
// the matrix
// ---------------------------------------------------------------------------

type probeMode int

const (
	// probeDeny: a 2xx is a breach. Used for the canonical single-object
	// read/mutate/delete and for actions that operate on one named object.
	probeDeny probeMode = iota
	// probeNoLeak: a 2xx is fine (an empty collection is the right answer);
	// the victim's marker string appearing in the body is the breach.
	probeNoLeak
)

type fenceProbe struct {
	method string
	// path may contain {placeholder} tokens resolved from fenceTenant.ids.
	path string
	body string
	mode probeMode
	// markerKind names which kind's marker a probeNoLeak probe should hunt for.
	// Empty means "this kind's own". A sub-collection needs it: the rows under
	// GET /crews/{crewId}/missions are missions, so the string that would prove
	// a leak is the MISSIONS marker, not the crews one. Getting this wrong is
	// how a probe ends up searching for a string the endpoint never renders,
	// which passes whatever the fence does.
	markerKind string
}

// marker returns the kind whose marker this probe proves a leak with.
func (p fenceProbe) markerFor(kindName string) string {
	if p.markerKind != "" {
		return p.markerKind
	}
	return kindName
}

type fenceKind struct {
	name string
	// seed inserts this kind's row(s) into ten's workspace and records the ids
	// it created under ten.ids.
	seed func(t *testing.T, db *sql.DB, ten *fenceTenant)
	// probes are run twice where the path has more than one placeholder: once
	// wholly against the victim, once grafting the victim's leaf onto the
	// attacker's own parent.
	probes []fenceProbe
	// positiveGet is a read the OWNING tenant must be able to perform. Without
	// it every "the attacker got 404" assertion could be satisfied by a route
	// that 404s for everybody — a broken handler reading as perfect isolation.
	// The same path is run as the victim, against the victim's own ids, and has
	// to come back 2xx.
	positiveGet string
	// positiveShowsMarker additionally requires the owner's response to CONTAIN
	// the marker. That is what makes the probeNoLeak assertions non-vacuous: it
	// proves the marker is a string this endpoint really renders from the
	// database, so its absence from the attacker's copy means something. Set it
	// wherever the owner's own read genuinely echoes the seeded name/title.
	positiveShowsMarker bool
	// table/idKey let the DELETE probes verify the victim row survived. Empty
	// table skips that check.
	table string
	idKey string
	// softDelete: the row carries a deleted_at tombstone rather than vanishing.
	softDelete bool
}

var fencePlaceholderRe = regexp.MustCompile(`\{([a-zA-Z0-9_]+)\}`)

// fenceWsSelf is the one placeholder that always resolves to the ATTACKER's own
// workspace. Routes that carry {workspaceId} in the path need the attacker to
// name a workspace they are legitimately a member of — otherwise
// RequireWorkspace 403s the request before the handler is reached and the probe
// proves nothing about the handler's own scoping.
const fenceWsSelf = "wsSelf"

// fenceSharedMemoryPath is the memory path BOTH tenants hold a version at, so
// the listing probe asks the question that matters: given a path I can guess,
// do I get my own row or theirs?
const fenceSharedMemoryPath = "agents/shared-slug/MEMORY.md"

// fenceOwnedPlaceholders counts the placeholders that resolve to a resource id
// (i.e. everything except {wsSelf}) — the graft variant only makes sense when
// there are at least two of those.
func fenceOwnedPlaceholders(path string) int {
	n := 0
	for _, m := range fencePlaceholderRe.FindAllStringSubmatch(path, -1) {
		if m[1] != fenceWsSelf {
			n++
		}
	}
	return n
}

// fenceSubst fills {placeholders}. When graft is true every resource
// placeholder except the last resolves from the attacker's own ids — the "my
// parent, your child" attack. Returns ok=false when a placeholder has no seeded
// id, so a kind that failed to seed fails loudly rather than being probed with
// an empty path segment (which would match a different route and pass
// vacuously).
func fenceSubst(path string, attacker, victim *fenceTenant, graft bool) (string, bool) {
	names := fencePlaceholderRe.FindAllStringSubmatch(path, -1)
	if len(names) == 0 {
		return path, true
	}
	owned := fenceOwnedPlaceholders(path)
	out := path
	seen := 0
	for _, m := range names {
		if m[1] == fenceWsSelf {
			out = strings.Replace(out, m[0], attacker.wsID, 1)
			continue
		}
		seen++
		src := victim
		if graft && seen < owned {
			src = attacker
		}
		id, ok := src.ids[m[1]]
		if !ok || id == "" {
			return "", false
		}
		out = strings.Replace(out, m[0], id, 1)
	}
	return out, true
}

// fenceDo issues one request as the attacker: their bearer token and their own
// workspace_id, so RequireWorkspace passes and the only thing under test is
// what the handler does with an id it does not own.
func fenceDo(t *testing.T, r *Router, attacker *fenceTenant, p fenceProbe, path string) *httptest.ResponseRecorder {
	t.Helper()
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	url := path + sep + "workspace_id=" + attacker.wsID
	var body *strings.Reader
	if p.body != "" {
		body = strings.NewReader(p.body)
	} else {
		body = strings.NewReader("")
	}
	req := httptest.NewRequest(p.method, url, body)
	req.Header.Set("Authorization", "Bearer "+attacker.token)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	// "not wired" / "not configured" means the handler bailed before any
	// tenancy check ran. That is a non-2xx, so a naive assertion would call it
	// a pass — the exact vacuous-pass shape the audit warns about. Fail loudly
	// instead: the fix is to wire the backend in this test, not to accept the
	// 503 as isolation.
	if rr.Code == http.StatusServiceUnavailable &&
		(strings.Contains(rr.Body.String(), "not wired") || strings.Contains(rr.Body.String(), "not configured")) {
		t.Fatalf("VACUOUS: %s %s answered 503 %q before reaching any workspace check — wire that backend in this test instead of counting the 503 as a denial",
			p.method, path, fenceTrim(rr.Body.String()))
	}
	return rr
}

// TestCrossWorkspaceFence_Matrix is the attack matrix. For every resource kind,
// a WS-A OWNER points every canonical operation at a WS-B id through the real
// router. Nothing may come back 2xx, nothing may mutate, and no collection may
// carry WS-B's marker.
func TestCrossWorkspaceFence_Matrix(t *testing.T) {
	ensureEncryptionKey(t)
	db := setupTestDB(t)
	attacker := fenceSeedTenant(t, db, "a")
	victim := fenceSeedTenant(t, db, "b")

	// The optional backends have to be wired or their routes answer 503 before
	// they ever reach a workspace check — a probe that "passes" because the
	// feature was switched off proves nothing. fenceDo hard-fails on exactly
	// that 503 shape so this wiring cannot silently rot.
	r, err := NewRouter(db, "this-is-a-32-char-test-secret-pad", newTestLogger(),
		WithOutputBasePath(t.TempDir()))
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	r.PipelinesHandler.SetScheduleStore(pipeline.NewScheduleStore(db))
	r.PipelinesHandler.SetWebhookStore(pipeline.NewWebhookStore(db))
	r.PipelinesHandler.SetRunner(fenceNoopRunner{})

	// Guard against a vacuous pass: if the kinds table ever empties out (a bad
	// merge, a renamed helper) this test must fail rather than report success
	// on zero probes.
	probes := 0

	positives := 0
	noLeakControls := 0
	for _, k := range fenceKinds() {
		k := k
		t.Run(k.name, func(t *testing.T) {
			// Positive control first: if the owning tenant cannot read its own
			// row, every 404 below is meaningless and this kind must be fixed
			// or its probes removed — not left to pass by accident.
			if k.positiveGet != "" {
				path, ok := fenceSubst(k.positiveGet, victim, victim, false)
				if !ok {
					t.Fatalf("positive control %s: a placeholder has no seeded id", k.positiveGet)
				}
				positives++
				rr := fenceDo(t, r, victim, fenceProbe{method: "GET", path: k.positiveGet}, path)
				if rr.Code < 200 || rr.Code >= 300 {
					t.Fatalf("POSITIVE CONTROL FAILED: the owning tenant's GET %s = %d, want 2xx. "+
						"Until this passes, the cross-workspace 404s below prove nothing — the route may simply be broken. body=%s",
						path, rr.Code, fenceTrim(rr.Body.String()))
				}
				if k.positiveShowsMarker && !strings.Contains(rr.Body.String(), victim.marker(k.name)) {
					t.Fatalf("POSITIVE CONTROL FAILED: the owning tenant's GET %s (200) does not contain its own marker %q. "+
						"The no-leak probes below look for that string, so they cannot detect anything. body=%s",
						path, victim.marker(k.name), fenceTrim(rr.Body.String()))
				}
			}

			for _, p := range k.probes {
				p := p
				variants := []struct {
					label string
					graft bool
				}{{"direct", false}}
				if fenceOwnedPlaceholders(p.path) > 1 {
					variants = append(variants, struct {
						label string
						graft bool
					}{"graft", true})
				}
				for _, v := range variants {
					v := v
					path, ok := fenceSubst(p.path, attacker, victim, v.graft)
					if !ok {
						t.Errorf("%s %s [%s]: a placeholder has no seeded id — the kind's seed func and its probes disagree",
							p.method, p.path, v.label)
						continue
					}
					t.Run(v.label+" "+p.method+" "+p.path, func(t *testing.T) {
						probes++

						// Structural guard, part 2: falsifiability, per probe.
						//
						// `!strings.Contains(body, marker)` passes trivially when
						// the marker is nowhere this endpoint renders — the
						// assertion cannot fail, so it proves nothing. Two probes
						// shipped in exactly that state (crew_connections named its
						// peer crew "Peer b"; runs seeded empty metadata) and
						// review caught them, not the suite.
						//
						// So each no-leak probe first runs the IDENTICAL request as
						// the owning tenant and requires the marker to be present.
						// That is the control: the same request, the same body
						// parsing, differing only in whose data it should see. If
						// the owner's copy shows the marker and the attacker's does
						// not, the absence means something.
						if p.mode == probeNoLeak {
							ownPath, ok := fenceSubst(p.path, victim, victim, false)
							if !ok {
								t.Fatalf("no-leak control for %s: a placeholder has no seeded id", p.path)
							}
							own := fenceDo(t, r, victim, p, ownPath)
							want := victim.marker(p.markerFor(k.name))
							if !strings.Contains(own.Body.String(), want) {
								t.Fatalf("UNFALSIFIABLE: the owning tenant's own %s %s (status %d) does not contain its marker %q. "+
									"The attacker-side assertion below is `body does not contain that string`, which then passes "+
									"whether or not the fence holds. Seed the marker into a field this endpoint renders, set "+
									"markerKind to the kind whose rows it lists, or make this a probeDeny probe. body=%s",
									p.method, ownPath, own.Code, want, fenceTrim(own.Body.String()))
							}
							noLeakControls++
						}

						rr := fenceDo(t, r, attacker, p, path)
						switch p.mode {
						case probeDeny:
							if rr.Code >= 200 && rr.Code < 300 {
								t.Fatalf("LEAKED: WS-A %s %s (WS-B id) returned %d — cross-tenant read/mutation. body=%s",
									p.method, path, rr.Code, fenceTrim(rr.Body.String()))
							}
							if rr.Code >= 500 {
								t.Errorf("%s %s = %d (5xx, not a deliberate denial); body=%s",
									p.method, path, rr.Code, fenceTrim(rr.Body.String()))
							}
						case probeNoLeak:
							leak := victim.marker(p.markerFor(k.name))
							if strings.Contains(rr.Body.String(), leak) {
								t.Fatalf("LEAKED: WS-A %s %s returned %d carrying WS-B's marker %q — cross-tenant content. body=%s",
									p.method, path, rr.Code, leak, fenceTrim(rr.Body.String()))
							}
							if rr.Code >= 500 {
								t.Errorf("%s %s = %d (5xx); body=%s", p.method, path, rr.Code, fenceTrim(rr.Body.String()))
							}
						}
					})
				}
			}

			// A rejected DELETE must also have left the victim's row alone —
			// a handler that 404s the response after having already run the
			// DELETE would otherwise pass the status assertion above.
			if k.table != "" && k.idKey != "" {
				if id, ok := victim.ids[k.idKey]; ok && id != "" {
					if !fenceRowIntact(t, db, k.table, id, k.softDelete) {
						t.Errorf("LEAKED: WS-B %s row %q was destroyed by a cross-workspace request", k.name, id)
					}
				}
			}
		})
	}

	if probes < 40 {
		t.Fatalf("only %d probes ran — the matrix collapsed (vacuous pass guard)", probes)
	}
	if positives < 8 {
		t.Fatalf("only %d positive controls ran — without them the 404s above are not evidence of isolation", positives)
	}
	if noLeakControls < 10 {
		t.Fatalf("only %d no-leak controls ran — without them the content assertions are not falsifiable", noLeakControls)
	}
	t.Logf("cross-workspace fence: %d probes across %d resource kinds, %d positive controls, %d no-leak controls",
		probes, len(fenceKinds()), positives, noLeakControls)
}

// fenceRowIntact reports whether the row is still present and not tombstoned.
func fenceRowIntact(t *testing.T, db *sql.DB, table, id string, soft bool) bool {
	t.Helper()
	if soft {
		var deletedAt sql.NullString
		// #nosec G202 -- table comes from the in-file kinds table, never user input.
		err := db.QueryRow("SELECT deleted_at FROM "+table+" WHERE id = ?", id).Scan(&deletedAt)
		if err != nil {
			t.Errorf("read %s.deleted_at for %q: %v", table, id, err)
			return false
		}
		return !deletedAt.Valid
	}
	var n int
	// #nosec G202 -- table comes from the in-file kinds table, never user input.
	if err := db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE id = ?", id).Scan(&n); err != nil {
		t.Errorf("count %s for %q: %v", table, id, err)
		return false
	}
	return n == 1
}

func fenceTrim(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// ---------------------------------------------------------------------------
// resource kinds — add a row here and the whole matrix applies to it
// ---------------------------------------------------------------------------

func fenceKinds() []fenceKind {
	return []fenceKind{
		{
			name:                "crews",
			positiveGet:         "/api/v1/crews/{crewId}",
			positiveShowsMarker: true,
			table:               "crews",
			idKey:               "crewId",
			// crews.Delete tombstones rather than removing.
			softDelete: true,
			seed: func(t *testing.T, db *sql.DB, ten *fenceTenant) {
				id := "crew-fence-" + ten.tag
				seedCrewRow(t, db, id, ten.wsID, ten.marker("crews"), "crew-"+ten.tag)
				ten.ids["crewId"] = id
				// A crew without a LEAD cannot accept an issue ("Crew has no
				// lead agent"), and a 400 for that reason would read as "the
				// fence held" while the write path never ran. Seed one so the
				// issue-create probes reach the code they are aimed at.
				ten.ids["leadAgentId"] = seedAgentRow(t, db, "lead-fence-"+ten.tag, ten.wsID, id,
					"Lead "+ten.tag, "lead-"+ten.tag, "LEAD")
			},
			probes: []fenceProbe{
				{method: "GET", path: "/api/v1/crews/{crewId}", mode: probeDeny},
				{method: "PATCH", path: "/api/v1/crews/{crewId}", body: `{"name":"pwned"}`, mode: probeDeny},
				{method: "PUT", path: "/api/v1/crews/{crewId}", body: `{"name":"pwned"}`, mode: probeDeny},
				{method: "DELETE", path: "/api/v1/crews/{crewId}", mode: probeDeny},
				{method: "GET", path: "/api/v1/crews/{crewId}/policy", mode: probeDeny},
				{method: "GET", path: "/api/v1/crews/{crewId}/persona", mode: probeDeny},
				{method: "PUT", path: "/api/v1/crews/{crewId}/policy", body: `{"autonomy_level":"open"}`, mode: probeDeny},
				{method: "GET", path: "/api/v1/crews/{crewId}/members", mode: probeDeny},
				{method: "GET", path: "/api/v1/crews/{crewId}/missions", mode: probeNoLeak, markerKind: "missions"},
				{method: "GET", path: "/api/v1/crews/{crewId}/escalations", mode: probeNoLeak, markerKind: "escalations"},
				{method: "GET", path: "/api/v1/crews", mode: probeNoLeak},
			},
		},
		{
			name:                "agents",
			positiveGet:         "/api/v1/agents/{agentId}",
			positiveShowsMarker: true,
			table:               "agents",
			idKey:               "agentId",
			softDelete:          true,
			seed: func(t *testing.T, db *sql.DB, ten *fenceTenant) {
				id := "agent-fence-" + ten.tag
				seedAgentRow(t, db, id, ten.wsID, ten.ids["crewId"], ten.marker("agents"), "agent-"+ten.tag, "AGENT")
				ten.ids["agentId"] = id
			},
			probes: []fenceProbe{
				{method: "GET", path: "/api/v1/agents/{agentId}", mode: probeDeny},
				{method: "PATCH", path: "/api/v1/agents/{agentId}", body: `{"name":"pwned"}`, mode: probeDeny},
				{method: "DELETE", path: "/api/v1/agents/{agentId}", mode: probeDeny},
				{method: "GET", path: "/api/v1/agents/{agentId}/persona", mode: probeDeny},
				{method: "PUT", path: "/api/v1/agents/{agentId}/persona", body: `{"persona":"pwned"}`, mode: probeDeny},
				{method: "POST", path: "/api/v1/agents/{agentId}/stop", mode: probeDeny},
				{method: "POST", path: "/api/v1/agents/{agentId}/webhook-secret/rotate", mode: probeDeny},
				{method: "GET", path: "/api/v1/agents/{agentId}/skills", mode: probeDeny},
				{method: "GET", path: "/api/v1/agents/{agentId}/credentials", mode: probeDeny},
				{method: "GET", path: "/api/v1/agents/{agentId}/chats", mode: probeNoLeak, markerKind: "chats"},
				{method: "GET", path: "/api/v1/agents/{agentId}/runs", mode: probeNoLeak, markerKind: "runs"},
				{method: "GET", path: "/api/v1/agents", mode: probeNoLeak},
			},
		},
		{
			name:                "credentials",
			positiveGet:         "/api/v1/credentials/{credentialId}",
			positiveShowsMarker: true,
			table:               "credentials",
			idKey:               "credentialId",
			softDelete:          true,
			seed: func(t *testing.T, db *sql.DB, ten *fenceTenant) {
				id := "cred-fence-" + ten.tag
				if _, err := db.Exec(`INSERT INTO credentials
					(id, workspace_id, name, encrypted_value, type, provider, scope, status, created_by, created_at, updated_at)
					VALUES (?, ?, ?, 'enc', 'SECRET', 'NONE', 'WORKSPACE', 'ACTIVE', ?, datetime('now'), datetime('now'))`,
					id, ten.wsID, ten.marker("credentials"), ten.userID); err != nil {
					t.Fatalf("seed credential: %v", err)
				}
				ten.ids["credentialId"] = id
				// A binding, so GET /credentials/bindings has a row to render.
				// The listing echoes credential_name, which is the marker — an
				// empty bindings table made that probe unfalsifiable.
				if _, err := db.Exec(`INSERT INTO credential_bindings
					(id, workspace_id, credential_id, scope, slot, created_by)
					VALUES (?, ?, ?, 'WORKSPACE', 'FENCE_SLOT', ?)`,
					"credbind-fence-"+ten.tag, ten.wsID, id, ten.userID); err != nil {
					t.Fatalf("seed credential binding: %v", err)
				}
			},
			probes: []fenceProbe{
				{method: "GET", path: "/api/v1/credentials/{credentialId}", mode: probeDeny},
				{method: "PATCH", path: "/api/v1/credentials/{credentialId}", body: `{"name":"pwned"}`, mode: probeDeny},
				{method: "PUT", path: "/api/v1/credentials/{credentialId}", body: `{"name":"pwned"}`, mode: probeDeny},
				{method: "DELETE", path: "/api/v1/credentials/{credentialId}", mode: probeDeny},
				{method: "POST", path: "/api/v1/credentials/{credentialId}/reveal", body: `{"reason":"because"}`, mode: probeDeny},
				{method: "POST", path: "/api/v1/credentials/{credentialId}/rotate", body: `{"value":"pwned"}`, mode: probeDeny},
				{method: "GET", path: "/api/v1/credentials/{credentialId}/audit", mode: probeDeny},
				{method: "GET", path: "/api/v1/credentials/{credentialId}/fields", mode: probeDeny},
				{method: "GET", path: "/api/v1/credentials/{credentialId}/rotations", mode: probeDeny},
				{method: "GET", path: "/api/v1/credentials", mode: probeNoLeak},
				{method: "GET", path: "/api/v1/credentials/bindings", mode: probeNoLeak},
			},
		},
		{
			name:                "projects",
			positiveGet:         "/api/v1/projects/{projectId}",
			positiveShowsMarker: true,
			table:               "projects",
			idKey:               "projectId",
			seed: func(t *testing.T, db *sql.DB, ten *fenceTenant) {
				id := "proj-fence-" + ten.tag
				if _, err := db.Exec(`INSERT INTO projects (id, workspace_id, name, slug) VALUES (?, ?, ?, ?)`,
					id, ten.wsID, ten.marker("projects"), "proj-"+ten.tag); err != nil {
					t.Fatalf("seed project: %v", err)
				}
				ten.ids["projectId"] = id
			},
			probes: []fenceProbe{
				{method: "GET", path: "/api/v1/projects/{projectId}", mode: probeDeny},
				{method: "PATCH", path: "/api/v1/projects/{projectId}", body: `{"name":"pwned"}`, mode: probeDeny},
				{method: "DELETE", path: "/api/v1/projects/{projectId}", mode: probeDeny},
				{method: "GET", path: "/api/v1/projects/{projectId}/stats", mode: probeDeny},
				{method: "GET", path: "/api/v1/projects/{projectId}/milestones", mode: probeDeny},
				{method: "GET", path: "/api/v1/projects", mode: probeNoLeak},
			},
		},
		{
			name:  "milestones",
			table: "milestones",
			idKey: "milestoneId",
			seed: func(t *testing.T, db *sql.DB, ten *fenceTenant) {
				id := "ms-fence-" + ten.tag
				if _, err := db.Exec(`INSERT INTO milestones (id, project_id, name) VALUES (?, ?, ?)`,
					id, ten.ids["projectId"], ten.marker("milestones")); err != nil {
					t.Fatalf("seed milestone: %v", err)
				}
				ten.ids["milestoneId"] = id
			},
			probes: []fenceProbe{
				{method: "PATCH", path: "/api/v1/milestones/{milestoneId}", body: `{"name":"pwned"}`, mode: probeDeny},
				{method: "DELETE", path: "/api/v1/milestones/{milestoneId}", mode: probeDeny},
				{method: "POST", path: "/api/v1/projects/{projectId}/milestones", body: `{"name":"grafted"}`, mode: probeDeny},
			},
		},
		{
			name:  "labels",
			table: "labels",
			idKey: "labelId",
			seed: func(t *testing.T, db *sql.DB, ten *fenceTenant) {
				id := "lab-fence-" + ten.tag
				if _, err := db.Exec(`INSERT INTO labels (id, workspace_id, name) VALUES (?, ?, ?)`,
					id, ten.wsID, ten.marker("labels")); err != nil {
					t.Fatalf("seed label: %v", err)
				}
				ten.ids["labelId"] = id
			},
			probes: []fenceProbe{
				{method: "PATCH", path: "/api/v1/labels/{labelId}", body: `{"name":"pwned"}`, mode: probeDeny},
				{method: "DELETE", path: "/api/v1/labels/{labelId}", mode: probeDeny},
				{method: "GET", path: "/api/v1/labels", mode: probeNoLeak},
			},
		},
		{
			name:  "saved_views",
			table: "saved_views",
			idKey: "viewId",
			seed: func(t *testing.T, db *sql.DB, ten *fenceTenant) {
				id := "view-fence-" + ten.tag
				if _, err := db.Exec(`INSERT INTO saved_views (id, workspace_id, user_id, name) VALUES (?, ?, ?, ?)`,
					id, ten.wsID, ten.userID, ten.marker("saved_views")); err != nil {
					t.Fatalf("seed saved view: %v", err)
				}
				ten.ids["viewId"] = id
			},
			probes: []fenceProbe{
				{method: "PATCH", path: "/api/v1/saved-views/{viewId}", body: `{"name":"pwned"}`, mode: probeDeny},
				{method: "DELETE", path: "/api/v1/saved-views/{viewId}", mode: probeDeny},
				{method: "GET", path: "/api/v1/saved-views", mode: probeNoLeak},
			},
		},
		{
			name:  "triage_rules",
			table: "triage_rules",
			idKey: "ruleId",
			seed: func(t *testing.T, db *sql.DB, ten *fenceTenant) {
				id := "trule-fence-" + ten.tag
				if _, err := db.Exec(`INSERT INTO triage_rules (id, workspace_id, name, pattern) VALUES (?, ?, ?, 'x')`,
					id, ten.wsID, ten.marker("triage_rules")); err != nil {
					t.Fatalf("seed triage rule: %v", err)
				}
				ten.ids["ruleId"] = id
			},
			probes: []fenceProbe{
				{method: "PATCH", path: "/api/v1/triage-rules/{ruleId}", body: `{"name":"pwned"}`, mode: probeDeny},
				{method: "DELETE", path: "/api/v1/triage-rules/{ruleId}", mode: probeDeny},
				{method: "GET", path: "/api/v1/triage-rules", mode: probeNoLeak},
			},
		},
		{
			name:  "recurring_issues",
			table: "recurring_issues",
			idKey: "recurringId",
			seed: func(t *testing.T, db *sql.DB, ten *fenceTenant) {
				id := "recur-fence-" + ten.tag
				if _, err := db.Exec(`INSERT INTO recurring_issues (id, workspace_id, crew_id, title, cron_expression)
					VALUES (?, ?, ?, ?, '0 8 * * *')`,
					id, ten.wsID, ten.ids["crewId"], ten.marker("recurring_issues")); err != nil {
					t.Fatalf("seed recurring issue: %v", err)
				}
				ten.ids["recurringId"] = id
			},
			probes: []fenceProbe{
				{method: "PATCH", path: "/api/v1/recurring-issues/{recurringId}", body: `{"title":"pwned"}`, mode: probeDeny},
				{method: "DELETE", path: "/api/v1/recurring-issues/{recurringId}", mode: probeDeny},
				{method: "GET", path: "/api/v1/recurring-issues", mode: probeNoLeak},
			},
		},
		{
			name:       "notification_channels",
			table:      "notification_channels",
			idKey:      "channelId",
			softDelete: true,
			seed: func(t *testing.T, db *sql.DB, ten *fenceTenant) {
				id := "nch-fence-" + ten.tag
				if _, err := db.Exec(`INSERT INTO notification_channels (id, workspace_id, type, config_json, created_by)
					VALUES (?, ?, 'webhook', ?, ?)`,
					id, ten.wsID, `{"url":"https://example.com/`+ten.marker("notification_channels")+`"}`, ten.userID); err != nil {
					t.Fatalf("seed notification channel: %v", err)
				}
				ten.ids["channelId"] = id
			},
			probes: []fenceProbe{
				{method: "PATCH", path: "/api/v1/notification-channels/{channelId}", body: `{"enabled":false}`, mode: probeDeny},
				{method: "DELETE", path: "/api/v1/notification-channels/{channelId}", mode: probeDeny},
				{method: "POST", path: "/api/v1/notification-channels/{channelId}/test", mode: probeDeny},
				{method: "POST", path: "/api/v1/notification-channels/{channelId}/agents", body: `{"agent_id":"x"}`, mode: probeDeny},
			},
		},
		{
			name:                "workflow_templates",
			positiveGet:         "/api/v1/templates/{templateId}",
			positiveShowsMarker: true,
			table:               "workflow_templates",
			idKey:               "templateId",
			seed: func(t *testing.T, db *sql.DB, ten *fenceTenant) {
				id := "wft-fence-" + ten.tag
				if _, err := db.Exec(`INSERT INTO workflow_templates (id, workspace_id, name, template_json)
					VALUES (?, ?, ?, '{}')`, id, ten.wsID, ten.marker("workflow_templates")); err != nil {
					t.Fatalf("seed workflow template: %v", err)
				}
				ten.ids["templateId"] = id
			},
			probes: []fenceProbe{
				{method: "GET", path: "/api/v1/templates/{templateId}", mode: probeDeny},
				{method: "PATCH", path: "/api/v1/templates/{templateId}", body: `{"name":"pwned"}`, mode: probeDeny},
				{method: "DELETE", path: "/api/v1/templates/{templateId}", mode: probeDeny},
				{method: "GET", path: "/api/v1/workflow-templates/{templateId}", mode: probeDeny},
				{method: "PATCH", path: "/api/v1/workflow-templates/{templateId}", body: `{"name":"pwned"}`, mode: probeDeny},
				{method: "DELETE", path: "/api/v1/workflow-templates/{templateId}", mode: probeDeny},
				{method: "GET", path: "/api/v1/templates", mode: probeNoLeak},
				{method: "GET", path: "/api/v1/workflow-templates", mode: probeNoLeak},
			},
		},
		{
			name:                "missions",
			positiveGet:         "/api/v1/crews/{crewId}/missions/{missionId}",
			positiveShowsMarker: true,
			table:               "missions",
			idKey:               "missionId",
			seed: func(t *testing.T, db *sql.DB, ten *fenceTenant) {
				id := "mission-fence-" + ten.tag
				if _, err := db.Exec(`INSERT INTO missions
					(id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, mission_type)
					VALUES (?, ?, ?, ?, ?, ?, 'PLANNING', 'orchestration')`,
					id, ten.wsID, ten.ids["crewId"], ten.ids["agentId"], "trace-"+ten.tag, ten.marker("missions")); err != nil {
					t.Fatalf("seed mission: %v", err)
				}
				ten.ids["missionId"] = id
			},
			probes: []fenceProbe{
				{method: "GET", path: "/api/v1/crews/{crewId}/missions/{missionId}", mode: probeDeny},
				{method: "PATCH", path: "/api/v1/crews/{crewId}/missions/{missionId}", body: `{"title":"pwned"}`, mode: probeDeny},
				{method: "DELETE", path: "/api/v1/crews/{crewId}/missions/{missionId}", mode: probeDeny},
				{method: "POST", path: "/api/v1/crews/{crewId}/missions/{missionId}/start", mode: probeDeny},
				{method: "POST", path: "/api/v1/crews/{crewId}/missions/{missionId}/clone", mode: probeDeny},
				{method: "POST", path: "/api/v1/missions/{missionId}/checkpoints", body: `{"label":"pwned"}`, mode: probeDeny},
				{method: "GET", path: "/api/v1/missions/{missionId}/checkpoints", mode: probeDeny},
				{method: "GET", path: "/api/v1/missions", mode: probeNoLeak},
			},
		},
		{
			name:                "issues",
			positiveGet:         "/api/v1/crews/{crewId}/issues/{issueIdent}",
			positiveShowsMarker: true,
			table:               "missions",
			idKey:               "issueId",
			seed: func(t *testing.T, db *sql.DB, ten *fenceTenant) {
				id := "issue-fence-" + ten.tag
				ident := strings.ToUpper(ten.tag) + "-1"
				if _, err := db.Exec(`INSERT INTO missions
					(id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, mission_type, number, identifier)
					VALUES (?, ?, ?, ?, ?, ?, 'PLANNING', 'issue', 1, ?)`,
					id, ten.wsID, ten.ids["crewId"], ten.ids["agentId"], "trace-issue-"+ten.tag,
					ten.marker("issues"), ident); err != nil {
					t.Fatalf("seed issue: %v", err)
				}
				ten.ids["issueId"] = id
				ten.ids["issueIdent"] = ident
			},
			probes: []fenceProbe{
				{method: "GET", path: "/api/v1/issues/{issueIdent}", mode: probeDeny},
				{method: "GET", path: "/api/v1/crews/{crewId}/issues/{issueIdent}", mode: probeDeny},
				{method: "PATCH", path: "/api/v1/crews/{crewId}/issues/{issueIdent}", body: `{"title":"pwned"}`, mode: probeDeny},
				{method: "DELETE", path: "/api/v1/crews/{crewId}/issues/{issueIdent}", mode: probeDeny},
				{method: "POST", path: "/api/v1/crews/{crewId}/issues/{issueIdent}/comments", body: `{"body":"pwned"}`, mode: probeDeny},
				{method: "POST", path: "/api/v1/crews/{crewId}/issues/{issueIdent}/start", mode: probeDeny},
				{method: "GET", path: "/api/v1/crews/{crewId}/issues/{issueIdent}/activity", mode: probeDeny},
				{method: "GET", path: "/api/v1/issues", mode: probeNoLeak},
			},
		},
		{
			name:  "chats",
			table: "chats",
			idKey: "chatId",
			seed: func(t *testing.T, db *sql.DB, ten *fenceTenant) {
				id := "chat-fence-" + ten.tag
				if _, err := db.Exec(`INSERT INTO chats (id, agent_id, workspace_id, created_by, title)
					VALUES (?, ?, ?, ?, ?)`,
					id, ten.ids["agentId"], ten.wsID, ten.userID, ten.marker("chats")); err != nil {
					t.Fatalf("seed chat: %v", err)
				}
				ten.ids["chatId"] = id
			},
			probes: []fenceProbe{
				{method: "DELETE", path: "/api/v1/agents/{agentId}/chats/{chatId}", mode: probeDeny},
				{method: "PUT", path: "/api/v1/agents/{agentId}/chats/{chatId}/read", mode: probeDeny},
				{method: "POST", path: "/api/v1/chats/{chatId}/steer", body: `{"message":"pwned"}`, mode: probeDeny},
				{method: "POST", path: "/api/v1/chats/{chatId}/participants", body: `{"user_id":"x"}`, mode: probeDeny},
				{method: "GET", path: "/api/v1/chats/{chatId}/messages", mode: probeDeny},
				{method: "GET", path: "/api/v1/chats/{chatId}/participants", mode: probeDeny},
			},
		},
		{
			// Seeded so GET /crews/{crewId}/escalations has something that COULD
			// leak — a no-leak probe against an empty table is not a test.
			name:  "escalations",
			table: "escalations",
			idKey: "escalationId",
			seed: func(t *testing.T, db *sql.DB, ten *fenceTenant) {
				id := "esc-fence-" + ten.tag
				if _, err := db.Exec(`INSERT INTO escalations
					(id, workspace_id, crew_id, chat_id, from_agent_id, reason, status, created_at)
					VALUES (?, ?, ?, ?, ?, ?, 'PENDING', strftime('%Y-%m-%dT%H:%M:%fZ','now'))`,
					id, ten.wsID, ten.ids["crewId"], ten.ids["chatId"], ten.ids["agentId"],
					ten.marker("escalations")); err != nil {
					t.Fatalf("seed escalation: %v", err)
				}
				ten.ids["escalationId"] = id
			},
			probes: []fenceProbe{
				{method: "PATCH", path: "/api/v1/escalations/{escalationId}/resolve", body: `{"resolution":"pwned"}`, mode: probeDeny},
			},
		},
		{
			name:                "checkpoints",
			positiveGet:         "/api/v1/checkpoints/{checkpointId}",
			positiveShowsMarker: true,
			table:               "checkpoints",
			idKey:               "checkpointId",
			seed: func(t *testing.T, db *sql.DB, ten *fenceTenant) {
				id := "ckpt-fence-" + ten.tag
				if _, err := db.Exec(`INSERT INTO checkpoints (id, workspace_id, crew_id, mission_id, label, journal_cursor)
					VALUES (?, ?, ?, ?, ?, '0')`,
					id, ten.wsID, ten.ids["crewId"], ten.ids["missionId"], ten.marker("checkpoints")); err != nil {
					t.Fatalf("seed checkpoint: %v", err)
				}
				ten.ids["checkpointId"] = id
			},
			probes: []fenceProbe{
				{method: "GET", path: "/api/v1/checkpoints/{checkpointId}", mode: probeDeny},
				{method: "DELETE", path: "/api/v1/checkpoints/{checkpointId}", mode: probeDeny},
				{method: "POST", path: "/api/v1/checkpoints/{checkpointId}/fork", body: `{}`, mode: probeDeny},
				{method: "POST", path: "/api/v1/checkpoints/{checkpointId}/restore", body: `{}`, mode: probeDeny},
			},
		},
		{
			name:                "crew_connections",
			table:               "crew_connections",
			idKey:               "connectionId",
			positiveGet:         "/api/v1/crew-connections",
			positiveShowsMarker: true,
			seed: func(t *testing.T, db *sql.DB, ten *fenceTenant) {
				// The peer crew carries the marker because that is what the
				// connection listing renders (to_crew_name). Naming it "Peer x"
				// made the no-leak probe unfalsifiable: the marker was nowhere in
				// the data, so strings.Contains could never fire.
				other := seedCrewRow(t, db, "crew-fence-peer-"+ten.tag, ten.wsID,
					ten.marker("crew_connections"), "crew-peer-"+ten.tag)
				ten.ids["peerCrewId"] = other
				id := "conn-fence-" + ten.tag
				if _, err := db.Exec(`INSERT INTO crew_connections (id, workspace_id, from_crew_id, to_crew_id)
					VALUES (?, ?, ?, ?)`, id, ten.wsID, ten.ids["crewId"], other); err != nil {
					t.Fatalf("seed crew connection: %v", err)
				}
				ten.ids["connectionId"] = id
			},
			probes: []fenceProbe{
				{method: "DELETE", path: "/api/v1/crew-connections/{connectionId}", mode: probeDeny},
				{method: "GET", path: "/api/v1/crew-connections", mode: probeNoLeak},
			},
		},
		{
			name:                "routines",
			positiveGet:         "/api/v1/workspaces/{wsSelf}/pipelines/{pipelineSlug}",
			positiveShowsMarker: true,
			table:               "pipelines",
			idKey:               "pipelineId",
			softDelete:          true,
			seed: func(t *testing.T, db *sql.DB, ten *fenceTenant) {
				id := "pipe-fence-" + ten.tag
				slug := "routine-" + ten.tag
				if _, err := db.Exec(`INSERT INTO pipelines
					(id, workspace_id, slug, name, definition_json, definition_hash, authored_via)
					VALUES (?, ?, ?, ?, '{"steps":[]}', 'hash-'||?, 'user_api')`,
					id, ten.wsID, slug, ten.marker("routines"), ten.tag); err != nil {
					t.Fatalf("seed pipeline: %v", err)
				}
				ten.ids["pipelineId"] = id
				ten.ids["pipelineSlug"] = slug
			},
			probes: []fenceProbe{
				// The attacker's own {workspaceId} in the path with the victim's
				// slug: the slug is unique per workspace, so resolving it without
				// the workspace filter is the hole.
				{method: "GET", path: "/api/v1/workspaces/{wsSelf}/pipelines/{pipelineSlug}", mode: probeDeny},
				{method: "GET", path: "/api/v1/workspaces/{wsSelf}/pipelines/{pipelineSlug}/export", mode: probeDeny},
				{method: "GET", path: "/api/v1/workspaces/{wsSelf}/pipelines/{pipelineSlug}/versions", mode: probeDeny},
				{method: "DELETE", path: "/api/v1/workspaces/{wsSelf}/pipelines/{pipelineSlug}", mode: probeDeny},
				{method: "POST", path: "/api/v1/workspaces/{wsSelf}/pipelines/{pipelineSlug}/disable", body: `{}`, mode: probeDeny},
				{method: "GET", path: "/api/v1/workspaces/{wsSelf}/pipelines", mode: probeNoLeak},
			},
		},
		{
			name:       "pipeline_schedules",
			table:      "pipeline_schedules",
			idKey:      "scheduleId",
			softDelete: true,
			seed: func(t *testing.T, db *sql.DB, ten *fenceTenant) {
				id := "psched-fence-" + ten.tag
				if _, err := db.Exec(`INSERT INTO pipeline_schedules
					(id, workspace_id, name, target_pipeline_id, cron_expr)
					VALUES (?, ?, ?, ?, '0 8 * * *')`,
					id, ten.wsID, ten.marker("pipeline_schedules"), ten.ids["pipelineId"]); err != nil {
					t.Fatalf("seed pipeline schedule: %v", err)
				}
				ten.ids["scheduleId"] = id
			},
			probes: []fenceProbe{
				// The schedule id is globally unique and the workspace comes
				// from the path — so the attacker supplies their OWN workspace
				// (which RequireWorkspace accepts) and the victim's schedule id.
				{method: "PATCH", path: "/api/v1/workspaces/{wsSelf}/pipeline-schedules/{scheduleId}", body: `{"enabled":false}`, mode: probeDeny},
				{method: "DELETE", path: "/api/v1/workspaces/{wsSelf}/pipeline-schedules/{scheduleId}", mode: probeDeny},
				{method: "POST", path: "/api/v1/workspaces/{wsSelf}/pipeline-schedules/{scheduleId}/run", body: `{}`, mode: probeDeny},
				{method: "GET", path: "/api/v1/workspaces/{wsSelf}/pipeline-schedules", mode: probeNoLeak},
			},
		},
		{
			name:       "pipeline_webhooks",
			table:      "pipeline_webhooks",
			idKey:      "webhookId",
			softDelete: true,
			seed: func(t *testing.T, db *sql.DB, ten *fenceTenant) {
				id := "pwh-fence-" + ten.tag
				if _, err := db.Exec(`INSERT INTO pipeline_webhooks
					(id, workspace_id, name, target_pipeline_id, token)
					VALUES (?, ?, ?, ?, ?)`,
					id, ten.wsID, ten.marker("pipeline_webhooks"), ten.ids["pipelineId"], "wtok-"+ten.tag); err != nil {
					t.Fatalf("seed pipeline webhook: %v", err)
				}
				ten.ids["webhookId"] = id
			},
			probes: []fenceProbe{
				{method: "DELETE", path: "/api/v1/workspaces/{wsSelf}/pipeline-webhooks/{webhookId}", mode: probeDeny},
				{method: "GET", path: "/api/v1/workspaces/{wsSelf}/pipeline-webhooks", mode: probeNoLeak},
			},
		},
		{
			name:                "inbox",
			positiveGet:         "/api/v1/inbox/{inboxId}",
			positiveShowsMarker: true,
			table:               "inbox_items",
			idKey:               "inboxId",
			seed: func(t *testing.T, db *sql.DB, ten *fenceTenant) {
				id := "inbox-fence-" + ten.tag
				if _, err := db.Exec(`INSERT INTO inbox_items (id, workspace_id, kind, source_id, title, target_user_id)
					VALUES (?, ?, 'message', ?, ?, ?)`,
					id, ten.wsID, "src-"+ten.tag, ten.marker("inbox"), ten.userID); err != nil {
					t.Fatalf("seed inbox item: %v", err)
				}
				ten.ids["inboxId"] = id
			},
			probes: []fenceProbe{
				{method: "GET", path: "/api/v1/inbox/{inboxId}", mode: probeDeny},
				{method: "PATCH", path: "/api/v1/inbox/{inboxId}", body: `{"state":"read"}`, mode: probeDeny},
				{method: "GET", path: "/api/v1/inbox", mode: probeNoLeak},
			},
		},
		{
			name:                "runs",
			positiveGet:         "/api/v1/runs/{runId}",
			table:               "",
			positiveShowsMarker: true,
			seed: func(t *testing.T, db *sql.DB, ten *fenceTenant) {
				runID := "run-fence-" + ten.tag
				// The marker rides in metadata, which runResponse renders. Passing
				// "" here (as this seed originally did) left the marker absent from
				// the data entirely, so the /api/v1/runs no-leak probe could not
				// have failed whatever the fence did.
				seedRunFixture(t, db, runID, ten.ids["agentId"], ten.wsID, "COMPLETED", "USER",
					`{"marker":"`+ten.marker("runs")+`"}`)
				ten.ids["runId"] = runID
			},
			probes: []fenceProbe{
				{method: "GET", path: "/api/v1/runs/{runId}", mode: probeDeny},
				{method: "GET", path: "/api/v1/runs", mode: probeNoLeak},
			},
		},
		{
			name:  "journal",
			table: "journal_entries",
			idKey: "journalId",
			// Split out of the runs kind: /api/v1/journal renders `summary`, not
			// the run metadata, so a journal probe hunting for the runs marker was
			// looking for a string this endpoint never emits. Each kind now seeds
			// its marker into a field its OWN endpoints render.
			positiveGet:         "/api/v1/journal",
			positiveShowsMarker: true,
			seed: func(t *testing.T, db *sql.DB, ten *fenceTenant) {
				id := "je-fence-" + ten.tag
				if _, err := db.Exec(`INSERT INTO journal_entries
					(id, workspace_id, agent_id, entry_type, severity, actor_type, summary, payload, refs)
					VALUES (?, ?, ?, 'note', 'info', 'system', ?, '{}', '{}')`,
					id, ten.wsID, ten.ids["agentId"], ten.marker("journal")); err != nil {
					t.Fatalf("seed journal entry: %v", err)
				}
				ten.ids["journalId"] = id
			},
			probes: []fenceProbe{
				{method: "GET", path: "/api/v1/journal/{journalId}", mode: probeDeny},
				{method: "GET", path: "/api/v1/journal", mode: probeNoLeak},
			},
		},
		{
			name:  "memory_versions",
			table: "memory_versions",
			idKey: "memoryVersionId",
			seed: func(t *testing.T, db *sql.DB, ten *fenceTenant) {
				id := "memver-fence-" + ten.tag
				// Both tenants keep a version at the SAME path. That is the
				// realistic case — "agents/<slug>/MEMORY.md" is a convention, not
				// a secret — and it is the one where a missing workspace filter
				// actually returns the other tenant's row rather than nothing.
				//
				// The marker rides in written_by, NOT in the path: this endpoint
				// echoes the requested path back in its response envelope, so a
				// marker in the path would appear in the body of a correctly-empty
				// result ({"count":0,"entries":null}) and the probe would fail on
				// its own input. A no-leak assertion is only sound when the marker
				// can reach the body solely from the database.
				//
				// No blob file is written: the list route reads the row, and the
				// content route has its own workspace comparison
				// (memory_versions_content_handler.go).
				if _, err := db.Exec(`INSERT INTO memory_versions
					(id, workspace_id, path, tier, sha256, bytes, written_at, written_by, payload_ref)
					VALUES (?, ?, ?, 'agent', ?, 12, strftime('%Y-%m-%dT%H:%M:%fZ','now'), ?, ?)`,
					id, ten.wsID, fenceSharedMemoryPath, "sha-"+ten.tag,
					ten.marker("memory_versions"), "/blobs/"+ten.tag); err != nil {
					t.Fatalf("seed memory version: %v", err)
				}
				ten.ids["memoryVersionId"] = id
			},
			positiveGet:         "/api/v1/memory/versions?path=" + fenceSharedMemoryPath,
			positiveShowsMarker: true,
			probes: []fenceProbe{
				{method: "GET", path: "/api/v1/memory/versions?path=" + fenceSharedMemoryPath, mode: probeNoLeak},
			},
		},
		{
			name:  "notifications",
			table: "notifications",
			idKey: "notificationId",
			seed: func(t *testing.T, db *sql.DB, ten *fenceTenant) {
				id := "notif-fence-" + ten.tag
				if _, err := db.Exec(`INSERT INTO notifications
					(id, workspace_id, user_id, actor_type, actor_id, action, entity_type, entity_id, entity_title)
					VALUES (?, ?, ?, 'system', 'sys', 'created', 'issue', ?, ?)`,
					id, ten.wsID, ten.userID, ten.ids["issueId"], ten.marker("notifications")); err != nil {
					t.Fatalf("seed notification: %v", err)
				}
				ten.ids["notificationId"] = id
			},
			probes: []fenceProbe{
				{method: "POST", path: "/api/v1/notifications/{notificationId}/read", mode: probeDeny},
				{method: "DELETE", path: "/api/v1/notifications/{notificationId}", mode: probeDeny},
			},
		},
	}
}
