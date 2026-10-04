package api

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/devcontainer"
)

func TestRequiredCLICheckGuardsRevisionPublication(t *testing.T) {
	for _, mode := range []string{"missing", "unavailable", "failed", "wrong image", "missing tool", "duplicate tool", "wrong pinned version", "passed", "record only"} {
		t.Run(mode, func(t *testing.T) {
			config := `{"image":"ubuntu:22.04"}`
			h, ws, crew := covProvRig(t, &covCommitClient{}, config)
			mise := `{"tools":{"codex":"0.152.0"},"ai_cli_check":"required"}`
			if mode == "record only" {
				mise = `{"tools":{"codex":"0.152.0"},"ai_cli_check":"record"}`
			}
			if _, err := h.db.Exec(`UPDATE crews SET mise_config=?,cached_image='working-image',config_hash='working-hash' WHERE id=?`, mise, crew); err != nil {
				t.Fatal(err)
			}
			if _, err := h.db.Exec(`INSERT INTO agents(id,workspace_id,crew_id,name,slug,cli_adapter) VALUES(?,?,?,?,?,?)`, "checked-agent", ws, crew, "Checked", "checked", "CODEX_CLI"); err != nil {
				t.Fatal(err)
			}
			result := revisionFixture()
			inv := result.Requirements.Toolchain
			result.CachedImage = inv.ImageID
			q := &devcontainer.ToolchainQualification{Status: "passed", ImageID: inv.ImageID, Tools: []devcontainer.ToolchainProbe{{Binary: "codex", Status: "passed"}}}
			inv.Qualification = q
			switch mode {
			case "missing", "record only":
				inv.Qualification = nil
			case "unavailable", "failed":
				q.Status = mode
			case "wrong image":
				q.ImageID = "sha256:" + strings.Repeat("b", 64)
			case "wrong pinned version":
				inv.Tools[0].Version = "0.153.0"
			case "missing tool":
				q.Tools = nil
			case "duplicate tool":
				q.Tools = append(q.Tools, q.Tools[0])
			}
			_, err := h.saveProvisionResult(context.Background(), crew, ws, provisionDefinition{Config: config, Mise: mise, Adapters: []string{"CODEX_CLI"}}, result)
			allowed := mode == "passed" || mode == "record only"
			if (err == nil) != allowed {
				t.Fatalf("publication allowed=%v, error=%v", allowed, err)
			}
			var image, hash string
			var count int
			if err := h.db.QueryRow(`SELECT cached_image,config_hash FROM crews WHERE id=?`, crew).Scan(&image, &hash); err != nil {
				t.Fatal(err)
			}
			if err := h.db.QueryRow(`SELECT COUNT(*) FROM environment_revisions WHERE crew_id=?`, crew).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if !allowed && (image != "working-image" || hash != "working-hash" || count != 0) {
				t.Fatalf("rejected evidence changed selected environment: %s %s %d", image, hash, count)
			}
			if allowed && (image != result.CachedImage || count != 1) {
				t.Fatal("valid result not published")
			}
		})
	}
}

func TestRequiredCLICheckFailsJobWithoutDiscardingWorkingImage(t *testing.T) {
	config := `{"image":"` + covLocalRef + `"}`
	mise := `{"ai_cli_check":"required"}`
	h, ws, crew := covProvRig(t, nil, config)
	h.provisioner = devcontainer.NewBuildOnlyProvisioner(requiredCheckBuilder{}, nil, newTestLogger())
	if _, err := h.db.Exec(`UPDATE crews SET mise_config=?,cached_image='working',config_hash='working-hash' WHERE id=?`, mise, crew); err != nil {
		t.Fatal(err)
	}
	job := covJob(crew)
	h.jobs[crew] = job
	h.rateLimiter.running[ws] = 1
	h.runProvisioning(crew, ws, config, mise, "", job)
	if job.Status != "failed" || !strings.Contains(job.Error, "AI CLI startup check required") || job.CompletedAt == nil {
		t.Fatalf("job=%+v", job)
	}
	if h.rateLimiter.running[ws] != 0 {
		t.Fatal("failed check retained capacity")
	}
	var image string
	if err := h.db.QueryRow(`SELECT cached_image FROM crews WHERE id=?`, crew).Scan(&image); err != nil || image != "working" {
		t.Fatalf("previous image=%q, %v", image, err)
	}
}

// A successful build-only backend has no offline Docker qualification. A
// required check must reject its result through the real job completion path.
type requiredCheckBuilder struct{}

func (requiredCheckBuilder) Available() bool                                           { return true }
func (requiredCheckBuilder) Build(context.Context, string, string, func(string)) error { return nil }
