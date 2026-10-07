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

func TestNativeInputReleaseRequiresConfirmedAbsenceAndMatchingVolume(t *testing.T) {
	for _, mode := range []string{"valid", "missing volume", "active consumer", "duplicate volumes", "bad labels", "bad inspection", "inspect failure", "list failure", "remove failure", "unconfirmed removal", "writer list failure", "duplicate writers", "bad writer"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			sum := sha256.Sum256([]byte("source"))
			manifest, err := NewNativeInputManifest([]NativeInputFile{{VersionID: "version", Name: "input.txt", Size: 6, SHA256: hex.EncodeToString(sum[:])}})
			must(err)
			p := Plan{Workspace: "workspace", Principal: "human", Agent: "agent", Scope: "scope", Attempt: "attempt", Revision: "revision", NativeSandbox: "protected", NativeInputs: manifest}
			p.Mounts = []Mount{{Resource: NativeInputResource(p), Target: NativeInputTarget, ReadOnly: true}}
			cli := filepath.Join(root, "docker")
			must(os.WriteFile(cli, []byte(`#!/bin/sh
set -eu
root=$(dirname "$0")
mode=$(cat "$root/mode")
printf '%s\n' "$*" >> "$root/log"
case "$1" in
ps)
 case "$4" in
 name=*)
  [ "$mode" != 'writer list failure' ] || exit 1
  if [ "$mode" = 'duplicate writers' ]; then printf 'one\ntwo\n'; fi
  if [ "$mode" = 'bad writer' ]; then printf 'writer\n'; fi ;;
 volume=*) if [ "$mode" = 'active consumer' ]; then printf 'consumer\n'; fi ;;
 *) exit 1 ;;
 esac ;;
inspect) printf '[]\n' ;;
volume)
 case "$2" in
 ls)
  [ "$mode" != 'list failure' ] || exit 1
  if [ "$mode" = 'duplicate volumes' ]; then printf 'one\ntwo\n'
  elif [ "$mode" != 'missing volume' ] && [ ! -f "$root/removed" ]; then printf 'volume\n'; fi ;;
 inspect)
  [ "$mode" != 'inspect failure' ] || exit 1
  cat "$root/volume.json" ;;
 rm)
  [ "$mode" != 'remove failure' ] || exit 1
  if [ "$mode" != 'unconfirmed removal' ]; then touch "$root/removed"; fi ;;
 *) exit 1 ;;
 esac ;;
*) exit 1 ;;
esac
`), 0700))
			must(os.WriteFile(filepath.Join(root, "mode"), []byte(mode), 0600))
			catalog, err := NewFrozenNativeCatalog(filepath.Join(root, "catalog"), Docker{Binary: cli}, func(context.Context, Plan) ([]NativeInputData, error) { return nil, ErrDenied })
			must(err)
			suffix := nativeInputObjectSuffix(p.Attempt)
			record := nativeSnapshotRecord{Attempt: p.Attempt, Resource: NativeInputResource(p), Workspace: p.Workspace, Scope: p.Scope, Revision: p.Revision, Provenance: p.provenance(), Fingerprint: p.fingerprint(), Volume: "crewship-rtest-input-" + catalog.owner + "-" + suffix, Populator: "crewship-rtest-input-writer-" + catalog.owner + "-" + suffix, State: "frozen"}
			must(catalog.save(record))
			labels := resourceLabels(p, record.Resource)
			labels[labelPrefix+"input-owner"] = catalog.owner
			if mode == "bad labels" {
				labels[labelPrefix+"workspace"] = "foreign"
			}
			raw, err := json.Marshal([]any{map[string]any{"Labels": labels}})
			must(err)
			if mode == "bad inspection" {
				raw = []byte("not-json")
			}
			must(os.WriteFile(filepath.Join(root, "volume.json"), raw, 0600))
			err = catalog.ReleaseNativeInputs(t.Context(), p)
			valid := mode == "valid" || mode == "missing volume"
			if (err == nil) != valid {
				t.Fatalf("release=%v for %s", err, mode)
			}
			stored, err := catalog.load(p.Attempt)
			must(err)
			if (stored.State == "retired") != valid {
				t.Fatalf("unconfirmed resource removal marked retired: %+v", stored)
			}
			trace, err := os.ReadFile(filepath.Join(root, "log"))
			must(err)
			if !valid && mode != "remove failure" && mode != "unconfirmed removal" && strings.Contains(string(trace), "volume rm") {
				t.Fatalf("untrusted volume reached destructive operation: %s", trace)
			}
		})
	}
}
