package main

// #2771: `crew delete` left the crew's auto-managed service credentials behind.
//
// A crew whose manifest declares a `services:` Redis gets an AUTO_MANAGED
// workspace credential REDIS_PASSWORD at apply time, tagged
// provisioned_for_service=<crew-slug>/redis. Deleting the crew removed its
// networks and containers and kept that row: nothing referenced it any more,
// and the next manifest that declared the same service answered "clashes with
// an existing workspace credential" until someone deleted it by hand.
//
// Driven through the BUILT BINARY against the REAL api router (the
// acceptance_credential_admin_test.go rig): `apply`, `crew delete` and
// `credential list` are the three commands the operator ran. The rule under
// test is narrow on purpose — only a credential the stored data proves was
// minted for the deleted crew's services goes; a user credential with the same
// name, and an auto-managed one another crew still uses, both stay.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// autoCredManifest is a minimal crew with one catalogued datastore. Redis is
// in the auto-managed catalog, so apply mints REDIS_PASSWORD for it.
func autoCredManifest(slug string) string {
	return `apiVersion: crewship/v1
kind: Crew
metadata:
  name: ` + slug + `
  slug: ` + slug + `
spec:
  services:
    - name: redis
      image: redis:7-alpine
  agents:
    - slug: ` + slug + `-lead
      name: Lead
      agent_role: LEAD
`
}

type listedCredential struct {
	ID                    string  `json:"id"`
	Name                  string  `json:"name"`
	Provider              string  `json:"provider"`
	ProvisionedForService *string `json:"provisioned_for_service"`
}

// credentialsByName lists the workspace's credentials through the CLI.
func credentialsByName(t *testing.T, rig *credAdminRig, cfgJSON string) map[string]listedCredential {
	t.Helper()
	stdout, combined, err := rig.exec(cfgJSON, "", "credential", "list", "--all")
	if err != nil {
		t.Fatalf("credential list: %v\n%s", err, combined)
	}
	var rows []listedCredential
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		var wrapped struct {
			Data        []listedCredential `json:"data"`
			Credentials []listedCredential `json:"credentials"`
		}
		if err2 := json.Unmarshal([]byte(stdout), &wrapped); err2 != nil {
			t.Fatalf("credential list did not print JSON: %v\n%s", err, combined)
		}
		rows = append(wrapped.Data, wrapped.Credentials...)
	}
	out := make(map[string]listedCredential, len(rows))
	for _, r := range rows {
		out[r.Name] = r
	}
	return out
}

func writeManifest(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func applyManifest(t *testing.T, rig *credAdminRig, cfg, path string, extra ...string) (string, error) {
	t.Helper()
	args := append([]string{"apply", "--file", path, "--yes"}, extra...)
	return rig.run(cfg, "", args...)
}

// The reported symptom, end to end: apply → delete → the credential is gone,
// and both a same-slug re-apply and a different crew with the same service go
// through without a duplicate.
func TestAcceptance_CrewDelete_RemovesItsAutoManagedCredential(t *testing.T) {
	rig := startCredAdminServer(t)
	cfgJSON := rig.config(credAdminOwnerToken, "json")
	cfgTable := rig.config(credAdminOwnerToken, "table")

	cacheA := writeManifest(t, "cache-a.yaml", autoCredManifest("cache-a"))
	if out, err := applyManifest(t, rig, cfgTable, cacheA); err != nil {
		t.Fatalf("apply cache-a: %v\n%s", err, out)
	}
	cred, ok := credentialsByName(t, rig, cfgJSON)["REDIS_PASSWORD"]
	if !ok || cred.Provider != "AUTO_MANAGED" || cred.ProvisionedForService == nil || *cred.ProvisionedForService != "cache-a/redis" {
		t.Fatalf("apply should mint REDIS_PASSWORD as AUTO_MANAGED for cache-a/redis, got %+v (present=%v)", cred, ok)
	}

	out := rig.must(cfgTable, "crew", "delete", "cache-a", "--yes")
	if !strings.Contains(out, "REDIS_PASSWORD") {
		t.Errorf("crew delete should name the auto-managed credential it removed:\n%s", out)
	}
	if _, still := credentialsByName(t, rig, cfgJSON)["REDIS_PASSWORD"]; still {
		t.Fatalf("REDIS_PASSWORD survived `crew delete cache-a`; it was minted for cache-a/redis and " +
			"nothing else uses it, so it is an orphan that blocks the next apply (#2771)")
	}

	// A different crew declaring the same service: this is the apply that
	// failed on dev2 with a duplicate credential.
	cacheB := writeManifest(t, "cache-b.yaml", autoCredManifest("cache-b"))
	if out, err := applyManifest(t, rig, cfgTable, cacheB); err != nil {
		t.Fatalf("apply cache-b after deleting cache-a must not clash with cache-a's credential: %v\n%s", err, out)
	}
	rig.must(cfgTable, "crew", "delete", "cache-b", "--yes")

	// The same manifest again, same slug.
	if out, err := applyManifest(t, rig, cfgTable, cacheA); err != nil {
		t.Fatalf("re-apply cache-a after delete: %v\n%s", err, out)
	}
	cred, ok = credentialsByName(t, rig, cfgJSON)["REDIS_PASSWORD"]
	if !ok || cred.ProvisionedForService == nil || *cred.ProvisionedForService != "cache-a/redis" {
		t.Fatalf("re-apply should mint a fresh REDIS_PASSWORD for cache-a/redis, got %+v (present=%v)", cred, ok)
	}
}

// A user's own REDIS_PASSWORD is not the crew's to delete, even when the crew
// runs a Redis sidecar that reads it.
func TestAcceptance_CrewDelete_KeepsUserCredentialWithTheSameName(t *testing.T) {
	rig := startCredAdminServer(t)
	cfgJSON := rig.config(credAdminOwnerToken, "json")
	cfgTable := rig.config(credAdminOwnerToken, "table")

	rig.must(cfgTable, "credential", "create", "--name", "REDIS_PASSWORD", "--type", "GENERIC_SECRET", "--value", "operator-chosen-secret")

	// The operator owns Redis auth here (an explicit --requirepass), so apply
	// mints nothing and the agent reads the operator's credential.
	manifest := writeManifest(t, "byo.yaml", `apiVersion: crewship/v1
kind: Crew
metadata:
  name: byo
  slug: byo
spec:
  credentials:
    - env: REDIS_PASSWORD
      provider: NONE
      type: GENERIC_SECRET
  services:
    - name: redis
      image: redis:7-alpine
      command: ["redis-server", "--requirepass", "operator-chosen-secret"]
  agents:
    - slug: byo-lead
      name: Lead
      agent_role: LEAD
      env_refs: [REDIS_PASSWORD]
`)
	if out, err := applyManifest(t, rig, cfgTable, manifest); err != nil {
		t.Fatalf("apply byo: %v\n%s", err, out)
	}
	before := credentialsByName(t, rig, cfgJSON)["REDIS_PASSWORD"]
	if before.ID == "" || before.Provider == "AUTO_MANAGED" {
		t.Fatalf("fixture: REDIS_PASSWORD should be the operator's row, got %+v", before)
	}

	rig.must(cfgTable, "crew", "delete", "byo", "--yes")

	after, ok := credentialsByName(t, rig, cfgJSON)["REDIS_PASSWORD"]
	if !ok || after.ID != before.ID {
		t.Fatalf("crew delete removed the operator's own REDIS_PASSWORD (%s); only rows minted "+
			"for the crew's services may go", before.ID)
	}
}

// An auto-managed credential another crew has been wired to is shared: the
// minting crew's delete leaves it for the crew still using it.
func TestAcceptance_CrewDelete_KeepsAutoCredentialAnotherCrewUses(t *testing.T) {
	rig := startCredAdminServer(t)
	cfgJSON := rig.config(credAdminOwnerToken, "json")
	cfgTable := rig.config(credAdminOwnerToken, "table")

	if out, err := applyManifest(t, rig, cfgTable, writeManifest(t, "owner.yaml", autoCredManifest("cache-owner"))); err != nil {
		t.Fatalf("apply cache-owner: %v\n%s", err, out)
	}
	consumer := writeManifest(t, "consumer.yaml", `apiVersion: crewship/v1
kind: Crew
metadata:
  name: consumer
  slug: consumer
spec:
  credentials:
    - env: REDIS_PASSWORD
      provider: NONE
      type: GENERIC_SECRET
  agents:
    - slug: consumer-lead
      name: Lead
      agent_role: LEAD
      env_refs: [REDIS_PASSWORD]
`)
	if out, err := applyManifest(t, rig, cfgTable, consumer); err != nil {
		t.Fatalf("apply consumer: %v\n%s", err, out)
	}
	before := credentialsByName(t, rig, cfgJSON)["REDIS_PASSWORD"]
	if before.ID == "" {
		t.Fatal("fixture: REDIS_PASSWORD missing after apply")
	}

	out := rig.must(cfgTable, "crew", "delete", "cache-owner", "--yes")
	after, ok := credentialsByName(t, rig, cfgJSON)["REDIS_PASSWORD"]
	if !ok || after.ID != before.ID {
		t.Fatalf("crew delete removed REDIS_PASSWORD although crew consumer's agent is bound to it:\n%s", out)
	}
}
