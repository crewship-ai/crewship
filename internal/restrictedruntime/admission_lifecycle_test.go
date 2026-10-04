//go:build linux

package restrictedruntime

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type admissionFixture struct {
	root      string
	manager   *Manager
	authority *fixtureAuthority
	plan      Plan
}

func writeAdmissionJSON(t *testing.T, name string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(name, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func newAdmissionFixture(t *testing.T) *admissionFixture {
	t.Helper()
	root := t.TempDir()
	script := `#!/bin/sh
set -eu
root=$(dirname "$0")
printf '%s\n' "$*" >> "$root/commands"
case "$1" in
image) [ ! -f "$root/fail-image" ]; cat "$root/image.json" ;;
create)
  touch "$root/present"
  [ ! -f "$root/fail-create" ]
  printf '%s\n' owned-container ;;
inspect)
  [ -f "$root/present" ]
  if [ "$2" = --format ]; then printf '%s\n' false
  else cat "$root/inspect.json"; fi ;;
start) [ ! -f "$root/fail-start" ] ;;
exec)
  case "$7" in
  lease) cat > "$root/lease.json"; [ ! -f "$root/fail-lease" ] ;;
  launch) cat > "$root/launch.json"; printf 'own output direct-canary end\n' ;;
  *) exit 1 ;;
  esac ;;
kill) exit 0 ;;
rm) [ ! -f "$root/fail-rm" ]; rm -f "$root/present" ;;
ps) [ ! -f "$root/fail-ps" ]; if [ -f "$root/present" ]; then printf '%s\n' owned-container; fi ;;
*) exit 1 ;;
esac
`
	binary := filepath.Join(root, "docker")
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	p := testPlan()
	p.Mounts = nil
	a := &fixtureAuthority{plans: map[string]Plan{"handle": p}, secrets: map[string]map[string]string{"handle": {"direct": "direct-canary"}}, denied: map[string]bool{}, ttl: 10 * time.Second}
	limits := Limits{128 << 20, 500000000, 48}
	m, err := New(filepath.Join(root, "state"), Docker{Binary: binary, Image: "pinned-image"}, a, catalogMap{}, limits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	writeAdmissionJSON(t, filepath.Join(root, "image.json"), []any{map[string]any{"ID": "sha256:pinned", "Config": map[string]any{"Volumes": map[string]any{}}}})
	writeAdmissionJSON(t, filepath.Join(root, "inspect.json"), []any{map[string]any{
		"Config":     map[string]any{"User": "1002:1002", "Entrypoint": []string{"/opt/crewship-runner"}, "Cmd": []string{"hold"}, "Labels": map[string]string{labelPrefix + "owner": m.owner, labelPrefix + "attempt": p.Attempt, labelPrefix + "plan": p.fingerprint()}},
		"HostConfig": map[string]any{"NetworkMode": "none", "CgroupnsMode": "private", "Runtime": "runc", "IpcMode": "private", "ReadonlyRootfs": true, "Init": true, "Memory": limits.MemoryBytes, "MemorySwap": limits.MemoryBytes, "NanoCpus": limits.NanoCPUs, "PidsLimit": limits.PIDs, "CapDrop": []string{"ALL"}, "SecurityOpt": []string{"no-new-privileges"}, "RestartPolicy": map[string]string{"Name": "no"}, "LogConfig": map[string]string{"Type": "none"}, "Tmpfs": privateTmpfs()},
	}})
	return &admissionFixture{root, m, a, p}
}
func (f *admissionFixture) fault(t *testing.T, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.root, name), nil, 0600); err != nil {
		t.Fatal(err)
	}
}
func (f *admissionFixture) trace(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.root, "commands"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(data)
}
func waitAdmissionDone(t *testing.T, s *Session) {
	t.Helper()
	select {
	case <-s.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("fixture runtime failed to finish")
	}
}
func admissionRecord(t *testing.T, f *admissionFixture) Record {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.manager.dir, f.plan.Attempt+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var r Record
	if err = json.Unmarshal(data, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAdmissionLaunchDeliveryReplayAndNewGeneration(t *testing.T) {
	f := newAdmissionFixture(t)
	s, err := f.manager.Start(t.Context(), "handle")
	if err != nil {
		t.Fatal(err)
	}
	waitAdmissionDone(t, s)
	if s.ID() != "owned-container" || s.Record().Status != "terminated" {
		t.Fatalf("runtime identity or cleanup lost: %+v", s.Record())
	}
	raw, err := os.ReadFile(filepath.Join(f.root, "launch.json"))
	if err != nil {
		t.Fatal(err)
	}
	var launch Bootstrap
	if err = json.Unmarshal(raw, &launch); err != nil {
		t.Fatal(err)
	}
	if strings.Join(launch.Command, " ") != strings.Join(f.plan.Command, " ") || launch.Env["DIRECT_TOKEN"] != "direct-canary" || launch.Files["direct"] != "direct-canary" {
		t.Fatalf("private bootstrap not delivered: %+v", launch)
	}
	out, err := s.Output(t.Context())
	if err != nil || out != "own output [REDACTED] end\n" {
		t.Fatalf("output=%q error=%v", out, err)
	}
	trace := f.trace(t)
	if strings.Contains(trace, "direct-canary") {
		t.Fatal("credential leaked into Docker argv")
	}
	if !strings.Contains(trace, "--user 1002:1002 owned-container /opt/crewship-runner lease") || !strings.Contains(trace, "--user 1001:1001 owned-container /opt/crewship-runner launch") {
		t.Fatalf("protected lease or agent UID lost: %s", trace)
	}
	before := trace
	if replay, e := f.manager.Start(t.Context(), "handle"); e == nil || replay != nil {
		t.Fatal("replayed completed attempt")
	}
	if f.trace(t) != before {
		t.Fatal("replay provisioned a container")
	}
	f.authority.mu.Lock()
	p := f.authority.plans["handle"]
	p.Attempt = "attempt2"
	p.Generation++
	f.authority.plans["handle"] = p
	f.authority.mu.Unlock()
	// The new container's inspection must reflect the exact new attempt and
	// authority fingerprint, while retaining the audited daemon configuration.
	inspectionJSON, e := os.ReadFile(filepath.Join(f.root, "inspect.json"))
	if e != nil {
		t.Fatal(e)
	}
	var inspection []map[string]any
	if e = json.Unmarshal(inspectionJSON, &inspection); e != nil {
		t.Fatal(e)
	}
	labels := inspection[0]["Config"].(map[string]any)["Labels"].(map[string]any)
	labels[labelPrefix+"attempt"] = p.Attempt
	labels[labelPrefix+"plan"] = p.fingerprint()
	writeAdmissionJSON(t, filepath.Join(f.root, "inspect.json"), inspection)
	retry, e := f.manager.Start(t.Context(), "handle")
	if e != nil {
		t.Fatal(e)
	}
	waitAdmissionDone(t, retry)
	if retry.Record().Attempt != "attempt2" || retry.Record().Status != "terminated" {
		t.Fatalf("new attempt failed: %+v", retry.Record())
	}
}

func TestAdmissionRechecksAuthorityAfterSecretResolution(t *testing.T) {
	for _, change := range []string{"revoked", "revision", "command", "scope"} {
		t.Run(change, func(t *testing.T) {
			f := newAdmissionFixture(t)
			f.authority.onSecrets = func() {
				if change == "revoked" {
					f.authority.denied["handle"] = true
					return
				}
				p := f.authority.plans["handle"]
				switch change {
				case "revision":
					p.Revision = "r2"
				case "command":
					p.Command = []string{"foreign-task"}
				case "scope":
					p.Scope = "other-scope"
				}
				f.authority.plans["handle"] = p
			}
			s, err := f.manager.Start(t.Context(), "handle")
			if s != nil || !errors.Is(err, ErrDenied) {
				t.Fatalf("changed authority admitted: %v", err)
			}
			if strings.Contains(f.trace(t), "/opt/crewship-runner launch") || strings.Contains(f.trace(t), "/opt/crewship-runner lease") {
				t.Fatal("delivered work after authority changed")
			}
			if r := admissionRecord(t, f); r.Status != "admission_failed" {
				t.Fatalf("failed admission status=%q", r.Status)
			}
			if _, err = os.Stat(filepath.Join(f.root, "present")); !os.IsNotExist(err) {
				t.Fatal("failed admission left owned container")
			}
		})
	}
}

func TestAdmissionRejectsInvalidCredentialMaterialBeforeLaunch(t *testing.T) {
	for _, tt := range []struct {
		name   string
		values map[string]string
	}{
		{"missing", nil}, {"unexpected", map[string]string{"direct": "valid", "extra": "foreign"}},
		{"wrong reference", map[string]string{"foreign": "value"}}, {"empty", map[string]string{"direct": ""}},
		{"NUL", map[string]string{"direct": "bad\x00value"}}, {"invalid UTF8", map[string]string{"direct": string([]byte{0xff})}},
		{"over budget", map[string]string{"direct": strings.Repeat("a", (1<<20)+1)}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newAdmissionFixture(t)
			f.authority.secrets["handle"] = tt.values
			s, err := f.manager.Start(t.Context(), "handle")
			if s != nil || !errors.Is(err, ErrDenied) {
				t.Fatalf("invalid credentials admitted: %v", err)
			}
			if strings.Contains(f.trace(t), "/opt/crewship-runner launch") {
				t.Fatal("invalid credentials reached worker")
			}
			if r := admissionRecord(t, f); r.Status != "admission_failed" {
				t.Fatalf("cleanup not recorded: %+v", r)
			}
		})
	}
}

func TestAdmissionProvisioningFailureRetainsUncertainty(t *testing.T) {
	for _, tt := range []struct {
		name, fault, status string
		reconciled          bool
	}{
		{"image unavailable", "fail-image", "admission_failed", true},
		{"lost create response", "fail-create", "termination_unconfirmed", false},
		{"start failure cleaned", "fail-start", "admission_failed", true},
		{"lease failure cleaned", "fail-lease", "admission_failed", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newAdmissionFixture(t)
			f.fault(t, tt.fault)
			s, err := f.manager.Start(t.Context(), "handle")
			if s != nil || err == nil {
				t.Fatal("faulty runtime admitted")
			}
			if r := admissionRecord(t, f); r.Status != tt.status {
				t.Fatalf("durable status=%q want %q", r.Status, tt.status)
			}
			if f.manager.reconciled != tt.reconciled {
				t.Fatalf("recovery gate=%v", f.manager.reconciled)
			}
			if strings.Contains(f.trace(t), "/opt/crewship-runner launch") {
				t.Fatal("failed provisioning launched work")
			}
			if !tt.reconciled {
				before := f.trace(t)
				f.authority.plans["handle"] = func() Plan { p := f.plan; p.Attempt = "retry"; return p }()
				if retry, e := f.manager.Start(t.Context(), "handle"); e == nil || retry != nil {
					t.Fatal("uncertain runtime admitted a fresh attempt")
				}
				if f.trace(t) != before {
					t.Fatal("recovery gate contacted Docker")
				}
			}
		})
	}
}

func TestAdmissionImageVolumesAndDaemonDriftWithholdCredentials(t *testing.T) {
	for _, kind := range []string{"image volumes", "invalid image ID", "daemon drift"} {
		t.Run(kind, func(t *testing.T) {
			f := newAdmissionFixture(t)
			switch kind {
			case "image volumes":
				writeAdmissionJSON(t, filepath.Join(f.root, "image.json"), []any{map[string]any{"ID": "sha256:pinned", "Config": map[string]any{"Volumes": map[string]any{"/foreign": map[string]any{}}}}})
			case "invalid image ID":
				writeAdmissionJSON(t, filepath.Join(f.root, "image.json"), []any{map[string]any{"ID": "tag-unpinned"}})
			case "daemon drift":
				writeAdmissionJSON(t, filepath.Join(f.root, "inspect.json"), []any{map[string]any{"HostConfig": map[string]any{"Privileged": true}}})
			}
			secretsRequested := false
			f.authority.onSecrets = func() { secretsRequested = true }
			s, err := f.manager.Start(t.Context(), "handle")
			if s != nil || !errors.Is(err, ErrDenied) {
				t.Fatalf("unsafe infrastructure admitted: %v", err)
			}
			if secretsRequested || strings.Contains(f.trace(t), "/opt/crewship-runner launch") {
				t.Fatal("secrets requested before audit succeeded")
			}
		})
	}
}
