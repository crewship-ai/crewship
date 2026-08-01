package api

// Regression coverage for #1627 — `container_memory_mb` / `container_cpus`
// reached the Docker daemon unvalidated.
//
// The wedge: `container_cpus: 0.005` passed the manifest, passed Create,
// passed Update (which had no check at all) and passed the provider's
// `<= 0` guard. The daemon then rejected the container create with
// "Range of CPUs is from 0.01" — an error that names neither the crew nor
// the field — and every agent run for that crew wedged on it.
//
// The other direction had no guard whatsoever: a MANAGER could set
// `container_memory_mb: 999999` and overcommit the host.
//
// #1638 revisits both bounds, in two tiers:
//
//   - HARD floor = Docker's own (6 MiB / 0.01 CPU). The daemon refuses these
//     outright, so failing early with a clear message is strictly better than
//     failing at container-create. Still a 400.
//   - ADVISORY floor = what one agent needs to run, an instance setting
//     defaulting to 2048 MiB / 0.5 CPU. Between the two the request is
//     ACCEPTED and the response carries a warning: the operator may have
//     chosen a small crew deliberately, and refusing does not make an
//     undersized crew any bigger — it only stops it existing.
//   - `container_memory_mb: 0` meant 4096 on create and 0-then-8192 on
//     update, so "reset to the server default" produced two different sizes.
//
// Every bound below is asserted against a LITERAL, never against the constant
// it is meant to pin. Asserting `wantMemory: minCrewContainerMemoryMB` is a
// tautology: raise the constant to nonsense and the test still passes. The
// literals here encode what the bound has to be WORTH, so moving a floor or a
// ceiling reddens this file.

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// crewResNew builds a CrewHandler over a freshly seeded user+workspace.
func crewResNew(t *testing.T) (*CrewHandler, *sql.DB, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	return NewCrewHandler(db, newTestLogger()), db, userID, wsID
}

// crewResWarnings pulls the advisory list off a crew create/update response.
//
// It decodes the raw body rather than a typed struct on purpose: the point of
// putting advisories on the response is that they reach a client over the
// wire, so the test has to read them the way a client would. A typed decode
// against our own struct would keep passing if the field never got marshalled.
func crewResWarnings(t *testing.T, body []byte) []string {
	t.Helper()
	var envelope struct {
		Slug     string   `json:"slug"`
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode crew response: %v (body %s)", err, body)
	}
	// Guard against decoding an error body as "no warnings": every success
	// response for these endpoints carries the crew, hence a slug.
	if envelope.Slug == "" {
		t.Fatalf("response carries no crew — reading warnings off it is meaningless (body %s)", body)
	}
	return envelope.Warnings
}

// setInstanceSetting upserts one app_settings row.
func setInstanceSetting(t *testing.T, db *sql.DB, key, value string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO app_settings (key, value, updated_at) VALUES (?, ?, datetime('now'))
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value); err != nil {
		t.Fatalf("set instance setting %s=%s: %v", key, value, err)
	}
}

// ---------------------------------------------------------------------------
// Create
// ---------------------------------------------------------------------------

func TestCrewCreate_RejectsOutOfRangeContainerResources(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string // substring the 400 must carry
	}{
		{
			name: "cpus below docker floor wedges every run",
			body: `{"name":"Wedged","slug":"wedged","container_cpus":0.005}`,
			want: "container_cpus",
		},
		{
			name: "negative cpus",
			body: `{"name":"Neg","slug":"neg","container_cpus":-1}`,
			want: "container_cpus",
		},
		{
			name: "cpus above ceiling",
			body: `{"name":"Huge","slug":"huge","container_cpus":100000}`,
			want: "container_cpus",
		},
		{
			name: "memory below docker floor",
			body: `{"name":"Tiny","slug":"tiny","container_memory_mb":4}`,
			want: "container_memory_mb",
		},
		{
			name: "negative memory",
			body: `{"name":"NegM","slug":"negm","container_memory_mb":-512}`,
			want: "container_memory_mb",
		},
		{
			name: "memory above ceiling overcommits the host",
			body: `{"name":"Fat","slug":"fat","container_memory_mb":999999}`,
			want: "container_memory_mb",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, db, userID, wsID := crewResNew(t)
			rr := covCruDoCreate(h, userID, wsID, "OWNER", tc.body)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("create = %d, want 400 (body %s)", rr.Code, rr.Body.String())
			}
			msg := rr.Body.String()
			if !strings.Contains(msg, tc.want) {
				t.Errorf("error %q does not name the offending field %q", msg, tc.want)
			}
			// The message must name the valid range, not just say "invalid" —
			// an operator has to be able to act on it without reading our source.
			if !strings.Contains(msg, "between") {
				t.Errorf("error %q does not name the valid range", msg)
			}
			// Nothing may be persisted on a rejected create.
			var n int
			if err := db.QueryRow(`SELECT COUNT(*) FROM crews WHERE workspace_id = ?`, wsID).Scan(&n); err != nil {
				t.Fatalf("count crews: %v", err)
			}
			if n != 0 {
				t.Errorf("rejected create persisted %d crew row(s)", n)
			}
		})
	}
}

func TestCrewCreate_AcceptsBoundaryAndDefaultContainerResources(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantMemory int
		wantCPUs   float64
	}{
		{
			// Literals, not the constants: the hard floor is only worth
			// something if it is THIS number — Docker's own minimum, the
			// point below which the daemon refuses the create.
			name:       "docker's hard floor exactly",
			body:       `{"name":"Floor","slug":"floor","container_memory_mb":6,"container_cpus":0.01}`,
			wantMemory: 6,
			wantCPUs:   0.01,
		},
		{
			// Between the hard floor and the advisory floor: accepted and
			// stored verbatim. The operator gets a warning, not a refusal.
			name:       "below the advisory floor is still accepted",
			body:       `{"name":"Small","slug":"small","container_memory_mb":1024,"container_cpus":0.25}`,
			wantMemory: 1024,
			wantCPUs:   0.25,
		},
		{
			name:       "advisory floor exactly",
			body:       `{"name":"Usable","slug":"usable","container_memory_mb":2048,"container_cpus":0.5}`,
			wantMemory: 2048,
			wantCPUs:   0.5,
		},
		{
			name:       "ceiling exactly",
			body:       `{"name":"Ceil","slug":"ceil","container_memory_mb":262144,"container_cpus":512}`,
			wantMemory: 262144,
			wantCPUs:   512,
		},
		{
			name:       "omitted falls back to the handler defaults",
			body:       `{"name":"Def","slug":"def"}`,
			wantMemory: 4096,
			wantCPUs:   2.0,
		},
		{
			// 0 is the "use the server default" sentinel the CLI and the
			// manifest both rely on (`--memory-mb 0`, omitted hostRequirements).
			// It predates this guard and must keep working.
			name:       "explicit zero keeps meaning server default",
			body:       `{"name":"Zero","slug":"zero","container_memory_mb":0,"container_cpus":0}`,
			wantMemory: 4096,
			wantCPUs:   2.0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, db, userID, wsID := crewResNew(t)
			rr := covCruDoCreate(h, userID, wsID, "OWNER", tc.body)
			if rr.Code != http.StatusCreated && rr.Code != http.StatusOK {
				t.Fatalf("create = %d, want 2xx (body %s)", rr.Code, rr.Body.String())
			}
			var gotMem int
			var gotCPUs float64
			if err := db.QueryRow(`SELECT container_memory_mb, container_cpus FROM crews WHERE workspace_id = ?`, wsID).
				Scan(&gotMem, &gotCPUs); err != nil {
				t.Fatalf("read crew: %v", err)
			}
			if gotMem != tc.wantMemory {
				t.Errorf("container_memory_mb = %d, want %d", gotMem, tc.wantMemory)
			}
			if gotCPUs != tc.wantCPUs {
				t.Errorf("container_cpus = %v, want %v", gotCPUs, tc.wantCPUs)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Update — the path that had no check at all
// ---------------------------------------------------------------------------

func TestCrewUpdate_RejectsOutOfRangeContainerResources(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"cpus below docker floor", `{"container_cpus":0.005}`, "container_cpus"},
		{"negative cpus", `{"container_cpus":-2}`, "container_cpus"},
		{"cpus above ceiling", `{"container_cpus":100000}`, "container_cpus"},
		{"memory below docker floor", `{"container_memory_mb":5}`, "container_memory_mb"},
		{"negative memory", `{"container_memory_mb":-1}`, "container_memory_mb"},
		{"memory above ceiling", `{"container_memory_mb":999999}`, "container_memory_mb"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, db, userID, wsID := crewResNew(t)
			crewID := seedCrewRow(t, db, "crew-res-upd", wsID, "Res", "res")

			rr := covCruDoUpdate(h, crewID, userID, wsID, "OWNER", tc.body)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("update = %d, want 400 (body %s)", rr.Code, rr.Body.String())
			}
			msg := rr.Body.String()
			if !strings.Contains(msg, tc.want) {
				t.Errorf("error %q does not name the offending field %q", msg, tc.want)
			}
			if !strings.Contains(msg, "between") {
				t.Errorf("error %q does not name the valid range", msg)
			}
			// The seeded 4096 / 2.0 must survive a rejected patch.
			var gotMem int
			var gotCPUs float64
			if err := db.QueryRow(`SELECT container_memory_mb, container_cpus FROM crews WHERE id = ?`, crewID).
				Scan(&gotMem, &gotCPUs); err != nil {
				t.Fatalf("read crew: %v", err)
			}
			if gotMem != 4096 || gotCPUs != 2.0 {
				t.Errorf("rejected patch mutated the row: memory=%d cpus=%v", gotMem, gotCPUs)
			}
		})
	}
}

func TestCrewUpdate_AcceptsBoundaryAndZeroContainerResources(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantMemory int
		wantCPUs   float64
	}{
		{
			name:       "docker's hard floor exactly",
			body:       `{"container_memory_mb":6,"container_cpus":0.01}`,
			wantMemory: 6,
			wantCPUs:   0.01,
		},
		{
			name:       "below the advisory floor is still accepted",
			body:       `{"container_memory_mb":1024,"container_cpus":0.25}`,
			wantMemory: 1024,
			wantCPUs:   0.25,
		},
		{
			name:       "advisory floor exactly",
			body:       `{"container_memory_mb":2048,"container_cpus":0.5}`,
			wantMemory: 2048,
			wantCPUs:   0.5,
		},
		{
			name:       "ceiling exactly",
			body:       `{"container_memory_mb":262144,"container_cpus":512}`,
			wantMemory: 262144,
			wantCPUs:   512,
		},
		{
			// #1638: `crewship crew update --memory-mb 0 --cpus 0` sends
			// explicit zeros. This used to STORE the zero, and the runtime's
			// own `<= 0` fallback then handed the crew 8 GiB — double what
			// the identical request produces on create, and double what the
			// docs promise. The handler now resolves the sentinel itself, so
			// the row carries the one documented default and no downstream
			// fallback gets a say.
			name:       "explicit zero resets to the same default create writes",
			body:       `{"container_memory_mb":0,"container_cpus":0}`,
			wantMemory: 4096,
			wantCPUs:   2.0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, db, userID, wsID := crewResNew(t)
			crewID := seedCrewRow(t, db, "crew-res-ok", wsID, "Res", "res")

			rr := covCruDoUpdate(h, crewID, userID, wsID, "OWNER", tc.body)
			if rr.Code != http.StatusOK {
				t.Fatalf("update = %d, want 200 (body %s)", rr.Code, rr.Body.String())
			}
			var gotMem int
			var gotCPUs float64
			if err := db.QueryRow(`SELECT container_memory_mb, container_cpus FROM crews WHERE id = ?`, crewID).
				Scan(&gotMem, &gotCPUs); err != nil {
				t.Fatalf("read crew: %v", err)
			}
			if gotMem != tc.wantMemory {
				t.Errorf("container_memory_mb = %d, want %d", gotMem, tc.wantMemory)
			}
			if gotCPUs != tc.wantCPUs {
				t.Errorf("container_cpus = %v, want %v", gotCPUs, tc.wantCPUs)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// #1638 — the two defects, asserted on behaviour
// ---------------------------------------------------------------------------

// "Reset to the server default" has to mean ONE size. Before #1638 the same
// `0` produced 4096 through create and a stored 0 through update, which the
// runtime's own `<= 0` fallback then read as 8192.
//
// The assertion is deliberately two-part: create and update must agree with
// each other AND both must equal the number the docs publish. Agreement alone
// would stay green if both paths drifted to the same wrong value.
func TestCrewZeroResources_CreateAndUpdateResolveToOneDefault(t *testing.T) {
	h, db, userID, wsID := crewResNew(t)

	// Create with the sentinel.
	rr := covCruDoCreate(h, userID, wsID, "OWNER",
		`{"name":"Created","slug":"created","container_memory_mb":0,"container_cpus":0}`)
	if rr.Code != http.StatusCreated && rr.Code != http.StatusOK {
		t.Fatalf("create = %d, want 2xx (body %s)", rr.Code, rr.Body.String())
	}
	var createMem int
	var createCPUs float64
	if err := db.QueryRow(`SELECT container_memory_mb, container_cpus FROM crews WHERE slug = 'created'`).
		Scan(&createMem, &createCPUs); err != nil {
		t.Fatalf("read created crew: %v", err)
	}

	// Patch an existing crew with the same sentinel.
	crewID := seedCrewRow(t, db, "crew-zero-upd", wsID, "Patched", "patched")
	rr = covCruDoUpdate(h, crewID, userID, wsID, "OWNER",
		`{"container_memory_mb":0,"container_cpus":0}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("update = %d, want 200 (body %s)", rr.Code, rr.Body.String())
	}
	var updateMem int
	var updateCPUs float64
	if err := db.QueryRow(`SELECT container_memory_mb, container_cpus FROM crews WHERE id = ?`, crewID).
		Scan(&updateMem, &updateCPUs); err != nil {
		t.Fatalf("read patched crew: %v", err)
	}

	if createMem != updateMem || createCPUs != updateCPUs {
		t.Errorf("`0` resolved to %d MiB / %v CPUs on create but %d MiB / %v CPUs on update; "+
			"the same request must produce the same crew", createMem, createCPUs, updateMem, updateCPUs)
	}
	// Literals: this is the size docs/api-reference/crews.mdx, docs/cli/crew.mdx
	// and docs/manifest/crew.md all publish as the server default.
	if createMem != 4096 || createCPUs != 2.0 {
		t.Errorf("resolved default = %d MiB / %v CPUs, want 4096 MiB / 2 CPUs (the documented server default)",
			createMem, createCPUs)
	}
	// And neither row may keep the sentinel: a stored 0 hands the decision to
	// whichever `<= 0` fallback the runtime happens to reach first.
	if updateMem == 0 || updateCPUs == 0 {
		t.Errorf("update stored the sentinel (memory=%d cpus=%v); the row must carry a resolved size",
			updateMem, updateCPUs)
	}
}

// The two tiers are different numbers doing different jobs, and both are
// pinned to literals.
//
// The HARD bound is the daemon's: refusing below it costs the operator nothing
// (the create would fail anyway) and buys a message that names the crew and
// the field. Raising it turns a warning into a refusal, which is the thing
// this design is specifically not supposed to do.
//
// The ADVISORY default is what one agent needs. It is only a DEFAULT here —
// the live value comes from the instance setting.
func TestCrewContainerBounds_HardIsDockerAdvisoryIsOneAgent(t *testing.T) {
	if dockerMinContainerMemoryMB != 6 {
		t.Errorf("dockerMinContainerMemoryMB = %d, want 6 — the daemon's own "+
			"\"Minimum memory limit allowed is 6MB\". A hard floor above the daemon's refuses "+
			"configurations the daemon would have accepted", dockerMinContainerMemoryMB)
	}
	if dockerMinContainerCPUs != 0.01 {
		t.Errorf("dockerMinContainerCPUs = %v, want 0.01 — the daemon's own \"Range of CPUs is from 0.01\"",
			dockerMinContainerCPUs)
	}
	if defaultAgentMinMemoryMB != 2048 {
		t.Errorf("defaultAgentMinMemoryMB = %d, want 2048 — a warmed agent CLI holds 1.5-2 GiB, "+
			"and 512 MiB was measured OOM-killing real runs", defaultAgentMinMemoryMB)
	}
	if defaultAgentMinCPUs != 0.5 {
		t.Errorf("defaultAgentMinCPUs = %v, want 0.5 — a quarter of the shipped 2.0 default and half "+
			"the 1.0 the sidecar gets for a redis", defaultAgentMinCPUs)
	}
	// The advisory floor must sit strictly above the hard floor, or the
	// warn-band is empty and the whole two-tier design collapses back to a
	// single refusal boundary.
	if defaultAgentMinMemoryMB <= dockerMinContainerMemoryMB {
		t.Errorf("advisory memory floor %d is not above the hard floor %d: nothing would ever warn",
			defaultAgentMinMemoryMB, dockerMinContainerMemoryMB)
	}
	if defaultAgentMinCPUs <= dockerMinContainerCPUs {
		t.Errorf("advisory cpu floor %v is not above the hard floor %v: nothing would ever warn",
			defaultAgentMinCPUs, dockerMinContainerCPUs)
	}
	// Ceilings are typo guards, not host limits — but a ceiling below a
	// realistic large host would reject legitimate configuration.
	if maxCrewContainerMemoryMB != 262144 {
		t.Errorf("maxCrewContainerMemoryMB = %d, want 262144 (256 GiB)", maxCrewContainerMemoryMB)
	}
	if maxCrewContainerCPUs != 512 {
		t.Errorf("maxCrewContainerCPUs = %v, want 512", maxCrewContainerCPUs)
	}
}

// A hard 400 must name the daemon as the authority, so nobody "fixes" it by
// raising it into the band that is supposed to warn.
func TestCrewContainerResourceError_NamesTheDaemonLimit(t *testing.T) {
	h, _, userID, wsID := crewResNew(t)

	rr := covCruDoCreate(h, userID, wsID, "OWNER",
		`{"name":"Five","slug":"five","container_memory_mb":5}`)
	msg := rr.Body.String()
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("create = %d, want 400 (body %s)", rr.Code, msg)
	}
	if !strings.Contains(msg, "6") || !strings.Contains(msg, "between") {
		t.Errorf("memory error %q does not name the valid range", msg)
	}
	if !strings.Contains(msg, "Docker") {
		t.Errorf("memory error %q does not say the floor is the daemon's own", msg)
	}
}

// ---------------------------------------------------------------------------
// #1638 — the advisory band: accept, and warn
// ---------------------------------------------------------------------------

// A warning nobody emits is the same defect class as a test that measures the
// wrong thing, so this asserts the warning REACHES THE CLIENT: it is on the
// response body of the very request that created the crew.
func TestCrewCreate_WarnsBelowTheAdvisoryFloorButStillCreates(t *testing.T) {
	h, db, userID, wsID := crewResNew(t)

	rr := covCruDoCreate(h, userID, wsID, "OWNER",
		`{"name":"Small","slug":"small","container_memory_mb":1024,"container_cpus":0.25}`)
	if rr.Code != http.StatusCreated && rr.Code != http.StatusOK {
		t.Fatalf("create = %d, want 2xx — an undersized crew is a warning, not a refusal (body %s)",
			rr.Code, rr.Body.String())
	}

	// The crew exists, at exactly the size asked for. Refusing would not have
	// made it bigger; silently resizing it would be worse.
	var mem int
	var cpus float64
	if err := db.QueryRow(`SELECT container_memory_mb, container_cpus FROM crews WHERE slug = 'small'`).
		Scan(&mem, &cpus); err != nil {
		t.Fatalf("read crew: %v", err)
	}
	if mem != 1024 || cpus != 0.25 {
		t.Fatalf("stored %d MiB / %v CPUs, want the requested 1024 / 0.25", mem, cpus)
	}

	warnings := crewResWarnings(t, rr.Body.Bytes())
	if len(warnings) != 2 {
		t.Fatalf("want a warning for each undersized field, got %d: %v", len(warnings), warnings)
	}
	joined := strings.Join(warnings, "\n")
	for _, want := range []string{
		"container_memory_mb", "1024", "2048", "OOM",
		"container_cpus", "0.25", "0.5",
		// The operator must be able to act on it without reading our source:
		// either raise the crew, or move the floor.
		"runtime.agent_min_memory_mb", "runtime.agent_min_cpus",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("warnings %q missing %q", joined, want)
		}
	}
}

// The other half of the same contract: a correctly sized crew must NOT be
// nagged, or operators learn to ignore the field.
func TestCrewCreate_NoWarningAtOrAboveTheAdvisoryFloor(t *testing.T) {
	for _, body := range []string{
		`{"name":"Exact","slug":"exact","container_memory_mb":2048,"container_cpus":0.5}`,
		`{"name":"Roomy","slug":"roomy","container_memory_mb":8192,"container_cpus":4}`,
		`{"name":"Default","slug":"default"}`,
	} {
		h, _, userID, wsID := crewResNew(t)
		rr := covCruDoCreate(h, userID, wsID, "OWNER", body)
		if rr.Code != http.StatusCreated && rr.Code != http.StatusOK {
			t.Fatalf("create = %d, want 2xx (body %s)", rr.Code, rr.Body.String())
		}
		if w := crewResWarnings(t, rr.Body.Bytes()); len(w) != 0 {
			t.Errorf("%s produced warnings %v; a properly sized crew must be quiet", body, w)
		}
	}
}

// Update carries the advisory too — a crew can be shrunk into the band long
// after it was created, and that is exactly when nobody is watching.
func TestCrewUpdate_WarnsWhenShrunkBelowTheAdvisoryFloor(t *testing.T) {
	h, db, userID, wsID := crewResNew(t)
	crewID := seedCrewRow(t, db, "crew-shrink", wsID, "Shrink", "shrink")

	rr := covCruDoUpdate(h, crewID, userID, wsID, "OWNER", `{"container_memory_mb":512}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("update = %d, want 200 (body %s)", rr.Code, rr.Body.String())
	}
	warnings := crewResWarnings(t, rr.Body.Bytes())
	if len(warnings) != 1 {
		t.Fatalf("want one memory warning, got %d: %v", len(warnings), warnings)
	}
	if !strings.Contains(warnings[0], "container_memory_mb") || !strings.Contains(warnings[0], "512") {
		t.Errorf("warning %q does not name the field and the offending value", warnings[0])
	}

	// Growing it back must clear the advisory.
	rr = covCruDoUpdate(h, crewID, userID, wsID, "OWNER", `{"container_memory_mb":4096}`)
	if w := crewResWarnings(t, rr.Body.Bytes()); len(w) != 0 {
		t.Errorf("resizing back above the floor still warned: %v", w)
	}
}

// The advisory floor is the operator's to move. This is the whole point of
// making it a setting rather than a constant: someone who knows their workload
// lowers it and stops being warned; someone running heavier CLIs raises it and
// starts being warned earlier.
func TestCrewSizing_AdvisoryFloorFollowsTheInstanceSetting(t *testing.T) {
	t.Run("lowered floor silences the warning", func(t *testing.T) {
		h, db, userID, wsID := crewResNew(t)
		setInstanceSetting(t, db, SettingAgentMinMemoryMB, "512")

		rr := covCruDoCreate(h, userID, wsID, "OWNER",
			`{"name":"Small","slug":"small","container_memory_mb":1024}`)
		if w := crewResWarnings(t, rr.Body.Bytes()); len(w) != 0 {
			t.Errorf("floor lowered to 512, but 1024 MiB still warned: %v", w)
		}
	})

	t.Run("raised floor warns a crew that was fine before", func(t *testing.T) {
		h, db, userID, wsID := crewResNew(t)
		setInstanceSetting(t, db, SettingAgentMinMemoryMB, "8192")

		rr := covCruDoCreate(h, userID, wsID, "OWNER",
			`{"name":"Mid","slug":"mid","container_memory_mb":4096}`)
		w := crewResWarnings(t, rr.Body.Bytes())
		if len(w) != 1 {
			t.Fatalf("floor raised to 8192, want 4096 MiB to warn, got %v", w)
		}
		if !strings.Contains(w[0], "8192") {
			t.Errorf("warning %q does not quote the configured floor", w[0])
		}
	})

	t.Run("cpu floor is settable too", func(t *testing.T) {
		h, db, userID, wsID := crewResNew(t)
		setInstanceSetting(t, db, SettingAgentMinCPUs, "0.1")

		rr := covCruDoCreate(h, userID, wsID, "OWNER",
			`{"name":"Slow","slug":"slow","container_cpus":0.25}`)
		if w := crewResWarnings(t, rr.Body.Bytes()); len(w) != 0 {
			t.Errorf("cpu floor lowered to 0.1, but 0.25 still warned: %v", w)
		}
	})
}

// Garbage in the settings table must not take the advisory (or, via the same
// value, the scheduler's budget divisor) down with it.
func TestCrewSizing_UnusableSettingFallsBackToTheDefault(t *testing.T) {
	for _, bad := range []string{"", "banana", "-1", "0", "999999999"} {
		h, db, userID, wsID := crewResNew(t)
		setInstanceSetting(t, db, SettingAgentMinMemoryMB, bad)

		if got := agentMinMemoryMB(t.Context(), db); got != 2048 {
			t.Errorf("setting %q yielded floor %d, want the 2048 default", bad, got)
		}
		// And the advisory still works off that default.
		rr := covCruDoCreate(h, userID, wsID, "OWNER",
			`{"name":"Small","slug":"small","container_memory_mb":1024}`)
		if w := crewResWarnings(t, rr.Body.Bytes()); len(w) != 1 {
			t.Errorf("setting %q: want the default floor to still warn 1024 MiB, got %v", bad, w)
		}
	}
}

// The coherence assertion. `computeCrewBudget` divides a crew's memory by "how
// much one agent needs" to decide how many runs fit; the advisory floor warns
// when a crew is below "how much one agent needs". Those were two constants
// that happened to be 2048 apiece. If they are genuinely one value, moving the
// setting has to move BOTH — that is what this proves.
func TestAgentMinMemory_IsOneValueForBudgetAndAdvisory(t *testing.T) {
	h, db, userID, wsID := crewResNew(t)
	crewID := seedCrewRow(t, db, "crew-budget", wsID, "Budget", "budget")
	if _, err := db.Exec(`UPDATE crews SET container_memory_mb = 4096, max_concurrent_agents = NULL WHERE id = ?`, crewID); err != nil {
		t.Fatalf("size crew: %v", err)
	}

	// Default 2048: 4096/2048 = 2 slots, and a 1024 MiB crew warns.
	got, err := computeCrewBudget(t.Context(), db, crewID)
	if err != nil {
		t.Fatalf("computeCrewBudget: %v", err)
	}
	if got != 2 {
		t.Fatalf("budget = %d, want 2 (4096 / the 2048 default)", got)
	}

	// Halve the setting. The budget must double — if it does not, the
	// scheduler is still reading its own private constant.
	setInstanceSetting(t, db, SettingAgentMinMemoryMB, "1024")
	got, err = computeCrewBudget(t.Context(), db, crewID)
	if err != nil {
		t.Fatalf("computeCrewBudget: %v", err)
	}
	if got != 4 {
		t.Errorf("budget = %d, want 4 (4096 / the 1024 setting); computeCrewBudget is not reading %s",
			got, SettingAgentMinMemoryMB)
	}
	// Same setting, same tier, other consumer: 1024 MiB no longer warns.
	rr := covCruDoCreate(h, userID, wsID, "OWNER",
		`{"name":"Small","slug":"small","container_memory_mb":1024}`)
	if w := crewResWarnings(t, rr.Body.Bytes()); len(w) != 0 {
		t.Errorf("the same setting that moved the budget did not move the advisory: %v", w)
	}
}

// The ceiling is not an ADMIN-only guard. A MANAGER can create crews, and
// #1627 called out exactly that role setting container_memory_mb: 999999.
func TestCrewCreate_ManagerCannotExceedContainerCeiling(t *testing.T) {
	h, _, userID, wsID := crewResNew(t)
	rr := covCruDoCreate(h, userID, wsID, "MANAGER",
		`{"name":"Fat","slug":"fat","container_memory_mb":999999}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("manager over-ceiling create = %d, want 400 (body %s)", rr.Code, rr.Body.String())
	}
}
