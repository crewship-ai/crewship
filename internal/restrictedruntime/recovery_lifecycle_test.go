//go:build linux

package restrictedruntime

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRecoveryRejectsForeignOwnersAndUnconfirmedExistence(t *testing.T) {
	for _, kind := range []string{"owned", "absent", "lost create response", "foreign owner", "foreign attempt", "changed plan", "malformed inspect", "daemon unavailable", "removal fails", "malformed record"} {
		t.Run(kind, func(t *testing.T) {
			f := newAdmissionFixture(t)
			r := Record{Attempt: f.plan.Attempt, Container: "owned-container", Fingerprint: f.plan.fingerprint(), Status: "running"}
			if kind == "lost create response" {
				r.Container = ""
			}
			if err := f.manager.save(&r); err != nil {
				t.Fatal(err)
			}
			if kind != "absent" && kind != "daemon unavailable" {
				f.fault(t, "present")
			}
			switch kind {
			case "foreign owner", "foreign attempt", "changed plan":
				data, err := os.ReadFile(filepath.Join(f.root, "inspect.json"))
				if err != nil {
					t.Fatal(err)
				}
				var row []struct {
					Config     map[string]any
					HostConfig map[string]any
				}
				if err = json.Unmarshal(data, &row); err != nil {
					t.Fatal(err)
				}
				labels := row[0].Config["Labels"].(map[string]any)
				key := map[string]string{"foreign owner": "owner", "foreign attempt": "attempt", "changed plan": "plan"}[kind]
				labels[labelPrefix+key] = "foreign"
				writeAdmissionJSON(t, filepath.Join(f.root, "inspect.json"), row)
			case "malformed inspect":
				if err := os.WriteFile(filepath.Join(f.root, "inspect.json"), []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			case "daemon unavailable":
				f.fault(t, "fail-ps")
			case "removal fails":
				f.fault(t, "fail-rm")
			case "malformed record":
				if err := os.WriteFile(filepath.Join(f.manager.dir, r.Attempt+".json"), []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.manager.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := New(f.manager.dir, f.manager.Docker, f.authority, catalogMap{}, f.manager.Limits)
			if err != nil {
				t.Fatal(err)
			}
			f.manager = reopened
			t.Cleanup(func() { _ = reopened.Close() })
			if reopened.reconciled {
				t.Fatal("restart trusted an old attempt without recovery")
			}
			before := f.trace(t)
			if session, e := reopened.Start(t.Context(), "handle"); session != nil || !errors.Is(e, ErrDenied) {
				t.Fatalf("unreconciled manager admitted: %v", e)
			}
			if f.trace(t) != before {
				t.Fatal("unreconciled admission contacted Docker")
			}
			err = reopened.Reconcile(t.Context())
			allowed := kind == "owned" || kind == "absent" || kind == "lost create response"
			if (err == nil) != allowed {
				t.Fatalf("recovery %s: %v", kind, err)
			}
			trace := f.trace(t)
			if !allowed {
				if reopened.reconciled {
					t.Fatal("failed recovery reopened admission")
				}
				if kind != "removal fails" && strings.Contains(trace, "rm -f") {
					t.Fatal("recovery removed an unverified container")
				}
				return
			}
			if !reopened.reconciled {
				t.Fatal("verified cleanup did not reopen admission")
			}
			record := admissionRecord(t, f)
			if record.Status != "terminated" || record.Reason != "reconciled" || record.Fingerprint != r.Fingerprint {
				t.Fatalf("recovery altered provenance or failed stop: %+v", record)
			}
			if kind == "lost create response" && !strings.Contains(trace, "rm -f crewship-rtest-"+reopened.owner+"-"+r.Attempt) {
				t.Fatal("lost create response not resolved by exact owned name")
			}
			if replay, e := reopened.Start(t.Context(), "handle"); e == nil || replay != nil {
				t.Fatal("recovery allowed replay of an old attempt")
			}
		})
	}
}

func TestUnconfirmedTerminationBlocksNewAdmission(t *testing.T) {
	f := newAdmissionFixture(t)
	f.fault(t, "fail-rm")
	session, err := f.manager.Start(t.Context(), "handle")
	if err != nil {
		t.Fatal(err)
	}
	waitAdmissionDone(t, session)
	if session.Record().Status != "termination_unconfirmed" {
		t.Fatalf("optimistic stop: %+v", session.Record())
	}
	f.authority.mu.Lock()
	p := f.plan
	p.Attempt = "next-attempt"
	f.authority.plans["handle"] = p
	f.authority.mu.Unlock()
	before := f.trace(t)
	if next, e := f.manager.Start(t.Context(), "handle"); next != nil || !errors.Is(e, ErrDenied) {
		t.Fatalf("unconfirmed runtime allowed new work: %v", e)
	}
	if f.trace(t) != before {
		t.Fatal("new attempt provisioned before physical cleanup")
	}
	if err = f.manager.Close(); err == nil {
		t.Fatal("manager close concealed unconfirmed termination")
	}
}

func TestManagerOwnerLockAndUnsafeStateDirectories(t *testing.T) {
	f := newAdmissionFixture(t)
	if second, err := New(f.manager.dir, f.manager.Docker, f.authority, catalogMap{}, f.manager.Limits); err == nil || second != nil {
		t.Fatal("two managers acquired one state directory")
	}
	for _, kind := range []string{"world readable", "symlink", "invalid owner"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			state := filepath.Join(root, "state")
			switch kind {
			case "world readable":
				if err := os.Mkdir(state, 0755); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(root, state); err != nil {
					t.Fatal(err)
				}
			case "invalid owner":
				if err := os.Mkdir(state, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(state, "owner"), []byte("../foreign"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if manager, err := New(state, f.manager.Docker, f.authority, catalogMap{}, f.manager.Limits); manager != nil || !errors.Is(err, ErrDenied) {
				t.Fatalf("unsafe state accepted: %v", err)
			}
			if kind == "invalid owner" {
				if err := os.WriteFile(filepath.Join(state, "owner"), []byte("fixed-owner"), 0600); err != nil {
					t.Fatal(err)
				}
				fixed, err := New(state, f.manager.Docker, f.authority, catalogMap{}, f.manager.Limits)
				if err != nil {
					t.Fatalf("failed constructor leaked ownership lock: %v", err)
				}
				if err = fixed.Close(); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
