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

import (
	"database/sql"
	"net/http"
	"strconv"
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
			name:       "docker floor exactly",
			body:       `{"name":"Floor","slug":"floor","container_memory_mb":6,"container_cpus":0.01}`,
			wantMemory: minCrewContainerMemoryMB,
			wantCPUs:   minCrewContainerCPUs,
		},
		{
			name:       "ceiling exactly",
			body:       `{"name":"Ceil","slug":"ceil","container_memory_mb":` + strconv.Itoa(maxCrewContainerMemoryMB) + `,"container_cpus":` + strconv.FormatFloat(maxCrewContainerCPUs, 'f', -1, 64) + `}`,
			wantMemory: maxCrewContainerMemoryMB,
			wantCPUs:   maxCrewContainerCPUs,
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
			name:       "docker floor exactly",
			body:       `{"container_memory_mb":6,"container_cpus":0.01}`,
			wantMemory: minCrewContainerMemoryMB,
			wantCPUs:   minCrewContainerCPUs,
		},
		{
			name:       "ceiling exactly",
			body:       `{"container_memory_mb":` + strconv.Itoa(maxCrewContainerMemoryMB) + `,"container_cpus":` + strconv.FormatFloat(maxCrewContainerCPUs, 'f', -1, 64) + `}`,
			wantMemory: maxCrewContainerMemoryMB,
			wantCPUs:   maxCrewContainerCPUs,
		},
		{
			// `crewship crew update --memory-mb 0 --cpus 0` sends explicit
			// zeros; the provider reads them as "fall back to the configured
			// default". Keep that reachable rather than 400-ing it.
			name:       "explicit zero resets to the provider default",
			body:       `{"container_memory_mb":0,"container_cpus":0}`,
			wantMemory: 0,
			wantCPUs:   0,
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
