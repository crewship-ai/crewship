//go:build linux

package restrictedruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeInputFreezeFencesAliasesAndRevokedSourcesBeforeVerifier(t *testing.T) {
	for _, scenario := range []string{"valid", "writable-consumer", "foreign-alias", "revoked-source"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			content := []byte("H1 source canary")
			sum := sha256.Sum256(content)
			manifest, err := NewNativeInputManifest([]NativeInputFile{{VersionID: "version1", Name: "input.txt", Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:])}})
			if err != nil {
				t.Fatal(err)
			}
			p := Plan{Workspace: "workspace", Principal: "h1", Agent: "agent", Scope: "h1-scope", Attempt: "attempt", Revision: "1", NativeSandbox: NativeSandboxFingerprint(), NativeInputs: manifest}
			p.Mounts = []Mount{{Resource: NativeInputResource(p), Target: NativeInputTarget, ReadOnly: true}}
			cli := filepath.Join(root, "docker")
			if err := os.WriteFile(cli, []byte(`#!/bin/sh
set -eu
root=$(dirname "$0")
printf '%s\n' "$*" >> "$root/log"
case "$1" in
ps)
  case "$4" in
  name=*) if [ -f "$root/writer" ]; then printf '%s\n' writer-id; fi ;;
  volume=*) printf '%s\n' consumer-id
    if [ -f "$root/writer" ]; then printf '%s\n' writer-id; fi
    if [ -f "$root/extra" ]; then printf '%s\n' extra-id; fi ;;
  *) exit 1 ;;
  esac ;;
inspect)
  case "$2" in
  consumer-id) cat "$root/consumer.json" ;;
  writer-id) cat "$root/writer.json" ;;
  extra-id) cat "$root/extra.json" ;;
  *) exit 1 ;;
  esac ;;
rm) [ "$3" = writer-id ]; rm "$root/writer" ;;
exec) cat > /dev/null ;;
*) exit 1 ;;
esac
`), 0700); err != nil {
				t.Fatal(err)
			}
			catalog, err := NewFrozenNativeCatalog(filepath.Join(root, "catalog"), Docker{Binary: cli}, func(context.Context, Plan) ([]NativeInputData, error) {
				if scenario == "revoked-source" {
					return nil, ErrDenied
				}
				return []NativeInputData{{VersionID: "version1", Content: content}}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			suffix := nativeInputObjectSuffix(p.Attempt)
			r := nativeSnapshotRecord{Attempt: p.Attempt, Resource: NativeInputResource(p), Workspace: p.Workspace, Scope: p.Scope, Revision: p.Revision, Provenance: p.provenance(), Fingerprint: p.fingerprint(), Volume: "crewship-rtest-input-" + catalog.owner + "-" + suffix, Populator: "crewship-rtest-input-writer-" + catalog.owner + "-" + suffix, State: "ready"}
			if err := catalog.save(r); err != nil {
				t.Fatal(err)
			}
			write := func(name string, value any) {
				t.Helper()
				b, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, name), b, 0600); err != nil {
					t.Fatal(err)
				}
			}
			row := func(id, name string, rw bool) []any {
				user, entry, mode := "1002:1002", "/opt/crewship-runner", "hold"
				if name == r.Populator {
					user, entry, mode = "1001:1001", "/opt/crewship-native-runner", "project-input-hold"
				}
				return []any{map[string]any{"ID": id, "Name": "/" + name, "State": map[string]any{"Running": true}, "Config": map[string]any{"User": user, "Entrypoint": []string{entry}, "Cmd": []string{mode}, "Labels": map[string]string{labelPrefix + "attempt": p.Attempt, labelPrefix + "plan": p.fingerprint(), labelPrefix + "input-owner": catalog.owner}}, "HostConfig": map[string]string{"NetworkMode": "none"}, "Mounts": []any{map[string]any{"Name": r.Volume, "Destination": NativeInputTarget, "RW": rw}}}}
			}
			write("consumer.json", row("consumer-id", "consumer", scenario == "writable-consumer"))
			write("writer.json", row("writer-id", r.Populator, true))
			write("extra.json", row("extra-id", "foreign-consumer", false))
			if err := os.WriteFile(filepath.Join(root, "writer"), nil, 0600); err != nil {
				t.Fatal(err)
			}
			if scenario == "foreign-alias" {
				if err := os.WriteFile(filepath.Join(root, "extra"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			}
			err = catalog.FreezeNativeInputs(t.Context(), p, "consumer-id")
			if (err == nil) != (scenario == "valid") {
				t.Fatalf("freeze=%v for %s", err, scenario)
			}
			log, readErr := os.ReadFile(filepath.Join(root, "log"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			trace := string(log)
			verification := strings.Index(trace, "verify-project-inputs")
			removal := strings.Index(trace, "rm -f writer-id")
			if scenario == "valid" {
				if removal < 0 || verification < removal {
					t.Fatal("verifier ran while a writable source alias survived", trace)
				}
				got, err := catalog.load(p.Attempt)
				if err != nil || got.State != "frozen" || got.Consumer != "consumer-id" {
					t.Fatal("freeze not durably recorded", err)
				}
			} else if verification >= 0 {
				t.Fatal("denied source reached verifier/model boundary", trace)
			}
		})
	}
}
