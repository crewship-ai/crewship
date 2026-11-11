package api

import (
	"log/slog"
	"strings"
	"testing"
)

func TestCheckupWarnsAboutForeachAgentContention(t *testing.T) {
	db := openSmokeDB(t)
	h := NewPipelineHandler(db, slog.Default(), nil, nil)
	seedSmokePipelineDef(t, db, "parallel-agent", `{"name":"parallel-agent","steps":[{"id":"batch","type":"foreach","foreach":{"items":"[1,2]","parallelism":2,"steps":[{"id":"classify","type":"agent_run","agent_slug":"agent_lead","prompt":"classify"}]}}]}`)
	code, resp := checkupOf(t, h, "parallel-agent")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	found := false
	for _, c := range resp.Checks {
		if strings.HasPrefix(c.Key, "foreach-agent-contention:") {
			found = true
			if c.Level != "warn" || c.Tab != "steps" || !strings.Contains(c.Detail, "steps[0].foreach.steps[0].agent_slug") {
				t.Fatalf("warning %+v", c)
			}
		}
	}
	if !found {
		t.Fatalf("missing warning %+v", resp)
	}

	if _, err := db.Exec(`UPDATE pipelines SET definition_json=replace(definition_json,'"parallelism":2','"parallelism":1') WHERE slug='parallel-agent'`); err != nil {
		t.Fatal(err)
	}
	code, resp = checkupOf(t, h, "parallel-agent")
	if code != 200 {
		t.Fatalf("status %d", code)
	}
	for _, c := range resp.Checks {
		if strings.HasPrefix(c.Key, "foreach-agent-contention:") {
			t.Fatalf("sequential warning: %+v", c)
		}
	}
}
