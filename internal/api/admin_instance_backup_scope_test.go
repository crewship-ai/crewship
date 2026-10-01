package api

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/backupplan"
)

// varLibDockerOps answers CopyFrom with one file under /var/lib and an empty
// tar for every other path, so a test can tell whether a run asked for the
// crew's system data at all.
type varLibDockerOps struct {
	stubDockerOps
	asked []string
}

func (d *varLibDockerOps) CopyFrom(_ context.Context, _ string, src string) (io.ReadCloser, error) {
	d.asked = append(d.asked, src)
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if src == backup.ContainerVarLibPath {
		body := []byte("redis dump")
		_ = tw.WriteHeader(&tar.Header{Name: "lib/redis/", Typeflag: tar.TypeDir, Mode: 0o755, ModTime: time.Now()})
		_ = tw.WriteHeader(&tar.Header{Name: "lib/redis/dump.rdb", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body)), ModTime: time.Now()})
		_, _ = tw.Write(body)
	}
	_ = tw.Close()
	return io.NopCloser(bytes.NewReader(buf.Bytes())), nil
}

// Complete recovery takes the crew's full scope — /var/lib included — in the
// instance executor just as in the workspace one, on every data day, not only
// on the days that also capture container environments (review B2).
func TestInstanceExecutorScopeLevelFollowsPreset(t *testing.T) {
	cases := []struct {
		name         string
		preset       string
		environments bool
		wantLevel    backup.ScopeLevel
		wantVarLib   bool
	}{
		{"complete on a data-only day", backup.PresetComplete, false, backup.ScopeLevelFull, true},
		{"workspace preset stays standard", backup.PresetWorkspace, false, backup.ScopeLevelStandard, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newInstanceFixture(t)
			seedCrewRow(t, f.db, "crew-scope", "ws-old", "Research", "research")
			ops := &varLibDockerOps{}
			h := f.r.instanceBackups
			h.SetRecovery(InstanceRecoveryConfig{
				OutputDir: t.TempDir(), DockerOps: ops,
				CrewContainerName: func(id, slug string) string { return "ctr-" + slug },
			})
			id, err := age.GenerateX25519Identity()
			if err != nil {
				t.Fatal(err)
			}
			e := instanceExecutor{h: h, db: f.db}
			r, err := e.Run(context.Background(), backupplan.RunSpec{
				RunID: "scope-" + tc.preset, Preset: tc.preset, Scope: backupplan.ScopeInstance,
				Environments: tc.environments,
				Recipients:   []age.Recipient{id.Recipient()}, Actor: backup.Actor{UserID: "boss", Role: "OWNER"},
			}, func(string) {})
			if err != nil {
				t.Fatal(err)
			}
			if r.Manifest.ScopeLevel != tc.wantLevel {
				t.Fatalf("scope level = %q, want %q", r.Manifest.ScopeLevel, tc.wantLevel)
			}
			var crew *backup.CrewSummary
			for i := range r.Manifest.Contents.Crews {
				if r.Manifest.Contents.Crews[i].Slug == "research" {
					crew = &r.Manifest.Contents.Crews[i]
				}
			}
			if crew == nil {
				t.Fatalf("crew research missing from the manifest (docker asked for %v)", ops.asked)
			}
			if crew.SystemIncluded != tc.wantVarLib {
				t.Fatalf("crew /var/lib included = %v, want %v (docker asked for %v)", crew.SystemIncluded, tc.wantVarLib, ops.asked)
			}
		})
	}
}
