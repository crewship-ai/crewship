package seeddata

import (
	"bytes"
	"fmt"
	"time"

	"gopkg.in/yaml.v3"

	pagesdemo "github.com/crewship-ai/crewship/examples/pages-apps"
	"github.com/crewship-ai/crewship/internal/pages"
)

// OperationsApp loads the same portable source bundle offered as an example.
// Fail at startup on malformed built-in assets rather than seed half a demo.
var OperationsApp = mustLoadOperationsApp()

func mustLoadOperationsApp() *pages.TransferImport {
	b, err := pages.ParseProjectTransfer(bytes.NewReader(pagesdemo.Bundle))
	if err != nil {
		panic(fmt.Sprintf("seeddata: Operations Lab: %v", err))
	}
	return b
}

func operationsPage() PageDef {
	b := OperationsApp.Page
	p := PageDef{Slug: b.Slug, Name: b.Name, Description: b.Description, Owner: "crew/ops", Project: OperationsApp.Project}
	for _, v := range b.Panels {
		panel := PagePanelDef{ID: v.ID, Schema: v.Schema, Title: v.Title, Icon: v.Icon, Tab: v.Tab, Owner: v.Owner, Producer: v.Producer, SLA: (time.Duration(v.SLASeconds) * time.Second).String(), Span: v.Span}
		if len(v.Actions) > 0 {
			panel.Actions = v.Actions
		}
		if len(v.Wake) > 0 {
			panel.Wake = v.Wake
		}
		if v.OnFailure != nil {
			panel.OnFailure = v.OnFailure
		}
		p.Panels = append(p.Panels, panel)
	}
	return p
}

func operationsRoutine() RoutineDef {
	var d map[string]interface{}
	if err := yaml.Unmarshal(pagesdemo.Routine, &d); err != nil {
		panic(err)
	}
	// The catalogue uses typed steps; keep its existing shape for validators.
	var steps []map[string]interface{}
	for _, v := range d["steps"].([]interface{}) {
		steps = append(steps, v.(map[string]interface{}))
	}
	d["steps"] = steps
	return RoutineDef{Slug: "pages-operations-sample", Name: "pages-operations-sample", Description: d["description"].(string), CrewSlug: "ops", Definition: d}
}
