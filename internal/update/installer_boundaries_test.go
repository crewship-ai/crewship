package update

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestFullInstallerPreparesOnlyExistingCompanions(t *testing.T) {
	t.Setenv("CREWSHIP_SKIP_SIGNATURE_VERIFY", "")
	f := newSigGateFixture(t, true)
	files := map[string]string{"crewship": "new server"}
	for _, name := range companions {
		files[name] = "new " + name
	}
	archive := buildTarGz(t, files)
	asset := AssetNameForTag("v9.9.9", false)
	checksums := []byte(fmt.Sprintf("%x  %s\n", sha256.Sum256(archive), asset))
	pki := newTestPKI(t, testIdentity, testIssuer)
	signatureVerifyOpts.Roots = pki.rootPool
	f.assets[asset] = archive
	f.assets["checksums.txt"] = checksums
	f.assets["checksums.txt.sig"] = []byte(pki.sign(t, checksums))
	f.assets["checksums.txt.pem"] = pki.leafPEM
	dir := t.TempDir()
	executable := filepath.Join(dir, "custom-server")
	writeUpdateFixture(t, executable, "old server")
	installed := filepath.Join(dir, companions[0])
	writeUpdateFixture(t, installed, "old companion")
	prepared, err := PrepareInstallerUpdate(t.Context(), "v9.9.9", executable, false, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	assertUpdateFile(t, executable, "old server")
	assertUpdateFile(t, installed, "old companion")
	if _, err := os.Stat(executable + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("prepare touched backups: %v", err)
	}
	result, err := prepared.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Replaced, []string{executable, installed}) {
		t.Fatalf("incorrect companion selection: %#v", result)
	}
	assertUpdateFile(t, executable, "new server")
	assertUpdateFile(t, installed, "new "+companions[0])
	for _, name := range companions[1:] {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("uninstalled companion introduced: %s %v", name, err)
		}
	}
}

func writeUpdateFixture(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0755); err != nil {
		t.Fatal(err)
	}
}

func assertUpdateFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Fatalf("%s = %q, %v; want %q", path, got, err, want)
	}
}

func TestPreparedUpdateCommitsAndRestoresWholeInstall(t *testing.T) {
	dir := t.TempDir()
	server, sidecar := filepath.Join(dir, "renamed-server"), filepath.Join(dir, "crewship-sidecar")
	writeUpdateFixture(t, server, "old server")
	writeUpdateFixture(t, sidecar, "old sidecar")
	writeUpdateFixture(t, server+".bak", "stale backup")
	writeUpdateFixture(t, server+".old", "stale parked binary")
	p := &PreparedUpdate{targets: []payloadTarget{{path: server, data: []byte("new server")}, {path: sidecar, data: []byte("new sidecar")}}, fromVersion: "1.0.0", toVersion: "2.0.0", backupPath: server + ".bak"}
	result, err := p.Commit()
	if err != nil {
		t.Fatal(err)
	}
	if result.FromVersion != "1.0.0" || result.ToVersion != "2.0.0" || result.BackupPath != server+".bak" || !reflect.DeepEqual(result.Replaced, []string{server, sidecar}) {
		t.Fatalf("incomplete result: %#v", result)
	}
	assertUpdateFile(t, server, "new server")
	assertUpdateFile(t, sidecar, "new sidecar")
	assertUpdateFile(t, server+".bak", "old server")
	assertUpdateFile(t, sidecar+".bak", "old sidecar")
	if _, err := os.Stat(server + ".old"); !os.IsNotExist(err) {
		t.Fatalf("stale swap artifact remains: %v", err)
	}
	if err := RestoreBackups(result.Replaced); err != nil {
		t.Fatal(err)
	}
	assertUpdateFile(t, server, "old server")
	assertUpdateFile(t, sidecar, "old sidecar")
	if leftovers, err := filepath.Glob(filepath.Join(dir, ".crewship-update-*")); err != nil || len(leftovers) != 0 {
		t.Fatalf("staging files leaked: %v %v", leftovers, err)
	}
}

func TestPreparedUpdateBackupFailureDoesNotSwapAnyTarget(t *testing.T) {
	for _, failure := range []string{"missing target", "blocked backup"} {
		t.Run(failure, func(t *testing.T) {
			dir := t.TempDir()
			server, sidecar := filepath.Join(dir, "server"), filepath.Join(dir, "sidecar")
			writeUpdateFixture(t, server, "original server")
			if failure == "blocked backup" {
				writeUpdateFixture(t, sidecar, "original sidecar")
				if err := os.Mkdir(sidecar+".bak", 0700); err != nil {
					t.Fatal(err)
				}
			}
			p := &PreparedUpdate{targets: []payloadTarget{{path: server, data: []byte("replacement")}, {path: sidecar, data: []byte("replacement")}}}
			if result, err := p.Commit(); err == nil || result != nil || !strings.Contains(err.Error(), "back up") {
				t.Fatalf("failed backup accepted: %#v %v", result, err)
			}
			assertUpdateFile(t, server, "original server")
			if failure == "blocked backup" {
				assertUpdateFile(t, sidecar, "original sidecar")
			}
		})
	}
}

func TestRestoreBackupsAttemptsLaterTargetsAfterFailure(t *testing.T) {
	dir := t.TempDir()
	missing, second := filepath.Join(dir, "missing"), filepath.Join(dir, "second")
	writeUpdateFixture(t, second, "new")
	writeUpdateFixture(t, second+".bak", "previous")
	if err := RestoreBackups([]string{missing, second}); err == nil || !strings.Contains(err.Error(), missing+".bak") {
		t.Fatalf("first failure not reported: %v", err)
	}
	assertUpdateFile(t, second, "previous")
}

func TestAtomicReplacementRefusesDirectoryAndMissingParent(t *testing.T) {
	dir := t.TempDir()
	for _, target := range []string{filepath.Join(dir, "missing", "server"), dir} {
		if err := atomicReplace(target, []byte("replacement")); err == nil {
			t.Fatalf("invalid target accepted: %s", target)
		}
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("failed write leaked staging file: %v %v", entries, err)
	}
}

func TestSignedInstallerUpdatePreservesRenamedExecutable(t *testing.T) {
	t.Setenv("CREWSHIP_SKIP_SIGNATURE_VERIFY", "")
	newSigGateFixture(t, true)
	executable := filepath.Join(t.TempDir(), "custom-crewship")
	writeUpdateFixture(t, executable, "original executable")
	result, err := ApplyInstallerUpdate(t.Context(), "v9.9.9", executable, true, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Replaced, []string{executable}) {
		t.Fatalf("renamed destination lost: %#v", result)
	}
	assertUpdateFile(t, executable, "#!fake-binary-v2")
	assertUpdateFile(t, executable+".bak", "original executable")
	if _, err := os.Stat(filepath.Join(filepath.Dir(executable), "crewship")); !os.IsNotExist(err) {
		t.Fatalf("unexpected canonical executable: %v", err)
	}
}

func TestFailedInstallerPreparationDoesNotTouchExistingBinary(t *testing.T) {
	for _, missing := range []string{"checksums.txt", "checksums.txt.pem"} {
		t.Run(missing, func(t *testing.T) {
			t.Setenv("CREWSHIP_SKIP_SIGNATURE_VERIFY", "")
			f := newSigGateFixture(t, true)
			delete(f.assets, missing)
			executable := filepath.Join(t.TempDir(), "server")
			writeUpdateFixture(t, executable, "original executable")
			if result, err := ApplyInstallerUpdate(t.Context(), "v9.9.9", executable, true, "1.0.0"); err == nil || result != nil || !strings.Contains(err.Error(), missing) {
				t.Fatalf("incomplete update accepted: %#v %v", result, err)
			}
			assertUpdateFile(t, executable, "original executable")
			if _, err := os.Stat(executable + ".bak"); !os.IsNotExist(err) {
				t.Fatalf("prepare changed backups: %v", err)
			}
		})
	}
}
