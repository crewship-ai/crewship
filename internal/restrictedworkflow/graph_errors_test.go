package restrictedworkflow

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/crewship-ai/crewship/internal/access"
)

type graphErrorProvider struct{}

func (graphErrorProvider) ProviderDelegationHash(context.Context, string, string, string) (string, error) {
	return "", access.ErrDenied
}
func (graphErrorProvider) FreezeWorkflowProvider(context.Context, *sql.Tx, string, string, string, int64) error {
	return access.ErrDenied
}
func (graphErrorProvider) BindProviderDelegation(context.Context, string, string, string, string, int64) error {
	return access.ErrDenied
}

func TestGraphCompilationErrorClasses(t *testing.T) {
	for _, state := range []string{"missing routine", "missing child", "missing agent", "unsupported", "closed database", "cancelled query"} {
		t.Run(state, func(t *testing.T) {
			db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "graph.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Close() })
			_, err = db.Exec(`CREATE TABLE pipelines(id TEXT PRIMARY KEY,workspace_id TEXT,slug TEXT,definition_json TEXT,author_crew_id TEXT,deleted_at TEXT,status TEXT); CREATE TABLE agents(id TEXT,workspace_id TEXT,slug TEXT,restricted_execution_profile TEXT,crew_id TEXT,deleted_at TEXT)`)
			if err != nil {
				t.Fatal(err)
			}
			raw := `{"dsl_version":"1.0","name":"graph","steps":[{"id":"child","type":"call_pipeline","pipeline_slug":"absent"}]}`
			if state == "unsupported" {
				raw = `{}`
			}
			if state == "missing agent" {
				raw = `{"dsl_version":"1.0","name":"graph","steps":[{"id":"work","type":"agent_run","agent_slug":"absent","prompt":"Work"}]}`
			}
			if state != "missing routine" {
				if _, err = db.Exec(`INSERT INTO pipelines(id,workspace_id,slug,definition_json,status) VALUES('root','workspace','root',?,'active')`, raw); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if state == "closed database" {
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if state == "cancelled query" {
				cancel()
			}
			service := &Service{db: db, providers: graphErrorProvider{}, authorityOnly: true}
			_, err = service.compileGraph(ctx, "member", "workspace", "root")
			switch state {
			case "unsupported":
				if !errors.Is(err, ErrUnsupported) {
					t.Fatalf("unsupported declaration: %v", err)
				}
			case "closed database":
				if err == nil || errors.Is(err, ErrDenied) || errors.Is(err, ErrUnsupported) {
					t.Fatalf("storage failure lost: %v", err)
				}
			case "cancelled query":
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancelled query lost: %v", err)
				}
			default:
				if !errors.Is(err, ErrDenied) {
					t.Fatalf("missing dependency should be opaque denial: %v", err)
				}
			}
		})
	}
}
