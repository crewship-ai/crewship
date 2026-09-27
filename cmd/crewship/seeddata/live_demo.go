package seeddata

import (
	_ "embed"
	"strings"

	"github.com/crewship-ai/crewship/internal/pages"
)

//go:embed live_page.tsx
var livePageTSX string

func livePage() PageDef {
	base := OperationsApp.Project
	project := &pages.SourceProject{Format: base.Format, Runtime: base.Runtime, Files: append([]pages.ProjectFile(nil), base.Files...)}
	for i := range project.Files {
		switch project.Files[i].Path {
		case "src/main.tsx":
			project.Files[i].Content = livePageTSX
		case "src/style.css":
			project.Files[i].Content = cataloguePageCSS
		}
	}
	return PageDef{Slug: "demo-live", Name: "Live container monitor", Description: "A Python process samples real container memory every five seconds and publishes directly to this Page.", Owner: "crew/ops", Project: project, Panels: []PagePanelDef{
		{ID: "memory", Schema: "metric.v1", Title: "Live container memory", Icon: "memory", Owner: "crew/ops", Producer: "script/demo-live", SLA: "20s", Span: 12},
		{ID: "control", Schema: "status.v1", Title: "Monitor controls", Icon: "clock", Owner: "crew/ops", Producer: "script/demo-fixture", SLA: "168h", Span: 12, Demo: map[string]interface{}{"items": []map[string]string{{"name": "Container monitor", "state": "ok", "label": "Ready to start"}}}, Actions: []map[string]interface{}{{"id": "start", "kind": "call", "label": "Start live monitor", "routine": "demo-live-start"}, {"id": "stop", "kind": "call", "label": "Stop monitor", "routine": "demo-live-stop"}}},
	}}
}
func liveRoutines() []RoutineDef {
	var out []RoutineDef
	for _, action := range []string{"start", "stop"} {
		out = append(out, RoutineDef{Slug: "demo-live-" + action, Name: strings.ToUpper(action[:1]) + action[1:] + " live container monitor", CrewSlug: "ops", Description: "Control a local Python monitor; samples run outside routines every five seconds for at most 15 minutes.", Definition: map[string]interface{}{"dsl_version": "1.0", "name": "demo-live-" + action, "concurrency_key": "demo-live-control", "max_concurrent": 1, "steps": []map[string]interface{}{{"id": "control", "type": "script", "timeout_seconds": 10, "script": map[string]interface{}{"path": "demo/business/live.py", "interpreter": "python3", "args": []string{action}}}}}})
	}
	return out
}
