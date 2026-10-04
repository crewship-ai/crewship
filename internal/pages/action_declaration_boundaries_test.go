package pages

import (
	"fmt"
	"strings"
	"testing"
)

func TestPageActionDeclarationsRefuseAmbiguousControls(t *testing.T) {
	for name, change := range map[string]func(*PanelAction){
		"long label": func(a *PanelAction) { a.Label = strings.Repeat("a", MaxActionLabelRunes+1) },
		"too many parameters": func(a *PanelAction) {
			a.Params = map[string]any{}
			for i := 0; i <= MaxParamsPerAction; i++ {
				a.Params[fmt.Sprint(i)] = i
			}
		},
		"empty confirmation title": func(a *PanelAction) { a.Confirm = &PanelActionConfirm{Body: "Confirm this"} },
		"long confirmation": func(a *PanelAction) {
			a.Confirm = &PanelActionConfirm{Title: "Confirm", Body: strings.Repeat("a", MaxConfirmTextRunes+1)}
		},
		"call with navigation": func(a *PanelAction) { a.Ref = &PanelEntityRef{Kind: "page", ID: "other"} },
		"call with toggle":     func(a *PanelAction) { a.Target = []string{"sluzby"} },
		"link with inputs": func(a *PanelAction) {
			a.Kind = ActionLink
			a.Routine = ""
			a.Ref = &PanelEntityRef{Kind: "page", ID: "other"}
			a.Inputs = []PanelInput{{Name: "x"}}
		},
		"toggle with routine":     func(a *PanelAction) { a.Kind = ActionToggle; a.Target = []string{"sluzby"} },
		"duplicate toggle target": func(a *PanelAction) { a.Kind = ActionToggle; a.Routine = ""; a.Target = []string{"sluzby", "sluzby"} },
		"too many inputs":         func(a *PanelAction) { a.Inputs = make([]PanelInput, MaxInputsPerAction+1) },
		"duplicate input":         func(a *PanelAction) { a.Inputs = []PanelInput{{Name: "x"}, {Name: "x"}} },
		"long input label": func(a *PanelAction) {
			a.Inputs = []PanelInput{{Name: "x", Label: strings.Repeat("a", MaxActionLabelRunes+1)}}
		},
		"too many choices": func(a *PanelAction) {
			a.Inputs = []PanelInput{{Name: "x", Type: "select", Options: make([]string, MaxSelectOptions+1)}}
		},
		"empty choice": func(a *PanelAction) { a.Inputs = []PanelInput{{Name: "x", Type: "select", Options: []string{" "}}} },
		"duplicate choice": func(a *PanelAction) {
			a.Inputs = []PanelInput{{Name: "x", Type: "select", Options: []string{"yes", "yes"}}}
		},
		"invalid boolean default": func(a *PanelAction) { a.Inputs = []PanelInput{{Name: "x", Type: "boolean", Default: "yes"}} },
	} {
		t.Run(name, func(t *testing.T) {
			a := callAction()
			change(&a)
			if err := actionsDoc(a).Validate(); err == nil {
				t.Fatal("stored an ambiguous action declaration")
			}
		})
	}
}

func TestStoredActionLookupAndDefaultResolution(t *testing.T) {
	action := callAction()
	action.Inputs = []PanelInput{{Name: "enabled", Type: "boolean", Default: "false"}, {Name: "note"}}
	doc := actionsDoc(action)
	if err := doc.Validate(); err != nil {
		t.Fatal(err)
	}
	panel, ok := doc.FindPanel("sluzby")
	if !ok {
		t.Fatal("declared panel unavailable")
	}
	stored, ok := panel.FindAction(action.ID)
	if !ok || stored.EffectiveStyle() != ActionStyleDefault {
		t.Fatal("declared action unavailable")
	}
	resolved, err := stored.ResolveInputs(map[string]any{"enabled": nil, "note": "ok"})
	if err != nil || resolved["enabled"] != false || resolved["note"] != "ok" {
		t.Fatalf("default resolution: %#v %v", resolved, err)
	}
	stored.Style = ActionStyleDanger
	if stored.EffectiveStyle() != ActionStyleDanger {
		t.Fatal("lost declared destructive style")
	}
	if panel, ok := doc.FindPanel("foreign"); ok || panel != nil {
		t.Fatal("resolved undeclared panel")
	}
	for _, value := range []any{true, "no", 17} {
		got, err := stored.ResolveInputs(map[string]any{"enabled": value})
		if value == true {
			if err != nil || got["enabled"] != true {
				t.Fatalf("boolean changed: %#v %v", got, err)
			}
		} else if err == nil {
			t.Fatalf("invalid boolean accepted: %#v", value)
		}
	}
	stored.Inputs = []PanelInput{{Name: "choice", Type: "select", Options: []string{"a"}}}
	if _, err := stored.ResolveInputs(map[string]any{"choice": 1}); err == nil {
		t.Fatal("numeric select accepted")
	}
	stored.Inputs = []PanelInput{{Name: "text"}}
	if _, err := stored.ResolveInputs(map[string]any{"text": 1}); err == nil {
		t.Fatal("numeric text accepted")
	}
	// A legacy malformed default must still fail on the dispatch path.
	stored.Inputs = []PanelInput{{Name: "enabled", Type: "boolean", Default: "invalid"}}
	if _, err := stored.ResolveInputs(nil); err == nil {
		t.Fatal("invalid stored default dispatched")
	}
}
