package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCrewFileAccess_AdmissionAndRevocation(t *testing.T) {
	h, db, _, root, from, to := covMsgRig(t)
	dir := filepath.Join(root, "crews", to, "shared")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.txt"), []byte("synthetic-report"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		level       string
		read, write int
	}{
		{"read_write", 200, 201}, {"read", 200, 403}, {"none", 403, 403},
	} {
		t.Run(tc.level, func(t *testing.T) {
			if _, err := db.Exec(`UPDATE crew_connections SET forward_file_access=? WHERE id='cm-conn'`, tc.level); err != nil {
				t.Fatal(err)
			}
			r := httptest.NewRequest("GET", "/x?requester_crew_id="+from+"&path=report.txt", nil)
			r.SetPathValue("crewId", to)
			w := httptest.NewRecorder()
			h.ReadFile(w, r)
			if w.Code != tc.read {
				t.Fatalf("read: %d %s", w.Code, w.Body.String())
			}
			r = covMsgUpload(t, to, from, "test.txt", "synthetic-upload", true)
			w = httptest.NewRecorder()
			h.WriteFile(w, r)
			if w.Code != tc.write {
				t.Fatalf("write: %d %s", w.Code, w.Body.String())
			}
			// File restrictions must not disable task communication.
			allowed, err := h.canCommunicate(r, from, to)
			if err != nil || !allowed {
				t.Fatalf("communication: %v %v", allowed, err)
			}
			allowed, err = h.canAccessSharedFiles(r, to, from, true)
			if err != nil || !allowed {
				t.Fatalf("reverse access was changed: %v %v", allowed, err)
			}
		})
	}
	if _, err := db.Exec(`UPDATE crews SET deleted_at=datetime('now') WHERE id=?`, to); err != nil {
		t.Fatal(err)
	}
	allowed, err := h.canAccessSharedFiles(httptest.NewRequest("GET", "/", nil), to, from, false)
	if err != nil || allowed {
		t.Fatalf("deleted crew admitted: %v %v", allowed, err)
	}
}

func TestCrewFileAccess_UpdateAuthorityAndCAS(t *testing.T) {
	h, db, user, ws, from, to := crewConnectionsRig(t)
	insertConnection(t, db, "access-link", ws, from, to, "bidirectional")
	for _, tc := range []struct {
		name, role, workspace, requester, level string
		version                                 int
		status                                  int
	}{
		{"member", "MEMBER", ws, from, "none", 1, 403},
		{"foreign workspace", "MANAGER", "other", from, "none", 1, 404},
		{"wrong subject", "MANAGER", ws, "other", "none", 1, 400},
		{"invalid level", "MANAGER", ws, from, "admin", 1, 400},
		{"missing version", "MANAGER", ws, from, "none", 0, 400},
		{"read only", "MANAGER", ws, from, "read", 1, 200},
		{"stale overwrite", "MANAGER", ws, from, "read_write", 1, 409},
		{"revoke", "MANAGER", ws, from, "none", 2, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(fmt.Sprintf(`{"requester_crew_id":%q,"level":%q,"expected_version":%d}`, tc.requester, tc.level, tc.version)))
			r = withWorkspaceUser(r, user, tc.workspace, tc.role)
			r.SetPathValue("connectionId", "access-link")
			w := httptest.NewRecorder()
			h.UpdateFileAccess(w, r)
			if w.Code != tc.status {
				t.Fatalf("got %d want %d: %s", w.Code, tc.status, w.Body.String())
			}
		})
	}
	var forward, reverse string
	var version int
	if err := db.QueryRow(`SELECT forward_file_access,reverse_file_access,access_version FROM crew_connections WHERE id='access-link'`).Scan(&forward, &reverse, &version); err != nil {
		t.Fatal(err)
	}
	if forward != "none" || reverse != "read_write" || version != 3 {
		t.Fatalf("unexpected state %s %s %d", forward, reverse, version)
	}
}

func TestCrewFileAccess_DeliveryCannotOverwriteSharedRoot(t *testing.T) {
	h, _, _, root, from, to := covMsgRig(t)
	dir := filepath.Join(root, "crews", to, "shared")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "project.sh")
	if err := os.WriteFile(target, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	h.WriteFile(w, covMsgUpload(t, to, from, "../../project.sh", "replacement", true))
	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "original" {
		t.Fatalf("delivery overwrote the recipient's shared project")
	}
}

func TestCrewFileAccess_DeleteRejectsStaleSettings(t *testing.T) {
	h, db, user, ws, from, to := crewConnectionsRig(t)
	insertConnection(t, db, "delete-access", ws, from, to, "bidirectional")
	if _, err := db.Exec(`UPDATE crew_connections SET forward_file_access='none', access_version=2 WHERE id='delete-access'`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		version string
		status  int
	}{{"bad", 400}, {"0", 400}, {"1", 409}, {"2", 204}, {"2", 404}} {
		r := httptest.NewRequest(http.MethodDelete, "/?expected_version="+tc.version, nil)
		r = withWorkspaceUser(r, user, ws, "MANAGER")
		r.SetPathValue("connectionId", "delete-access")
		w := httptest.NewRecorder()
		h.Delete(w, r)
		if w.Code != tc.status {
			t.Fatalf("version %s: got %d want %d: %s", tc.version, w.Code, tc.status, w.Body.String())
		}
	}
}

func TestCrewFileAccess_CannotImpersonateSiblingRequester(t *testing.T) {
	h, db, ws, root, from, to := covMsgRig(t)
	intruder := seedCrewRow(t, db, "crew-intruder", ws, "Intruder", "intruder")
	dir := filepath.Join(root, "crews", to, "shared")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "report.txt"), []byte("synthetic"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, identity := range []struct {
		crew        string
		read, write int
	}{{intruder, 403, 403}, {from, 200, 201}} {
		for _, write := range []bool{false, true} {
			r := covMsgReadReq(to, "report.txt", from)
			if write {
				r = covMsgUpload(t, to, from, "report.txt", "synthetic", true)
			}
			ctx := context.WithValue(r.Context(), ctxInternalTokenWS, ws)
			ctx = context.WithValue(ctx, ctxInternalTokenCrew, identity.crew)
			r = r.WithContext(ctx)
			w := httptest.NewRecorder()
			if write {
				h.WriteFile(w, r)
			} else {
				h.ReadFile(w, r)
			}
			want := identity.read
			if write {
				want = identity.write
			}
			if w.Code != want {
				t.Errorf("identity=%s write=%v status=%d want=%d", identity.crew, write, w.Code, want)
			}
		}
	}
}
