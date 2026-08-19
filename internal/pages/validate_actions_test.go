package pages

import (
	"errors"
	"strings"
	"testing"
)

// Actions — the authoring gate (§8b.1, §8 rules 3, 4, 5, 9).
//
// The stored spec IS the dispatch allow-list (§8b.2), so every refusal here is a
// button that can never be clicked. That is what these tests are pinning: not
// "the parser rejects bad YAML" but "an action the vocabulary does not admit
// never reaches the table the dispatcher reads from".
//
// Each refusal case asserts on a substring of the message as well as on the
// error, because a validator that refuses for the wrong reason is a validator
// that will accept the right shape of the wrong thing after the next edit.

// actionsDoc builds a two-panel page and hangs the given actions off the first.
// Two panels so a toggle has somewhere legitimate to point.
func actionsDoc(actions ...PanelAction) *Document {
	return &Document{
		APIVersion: DocumentAPIVersion,
		Kind:       DocumentKind,
		Metadata:   Metadata{Name: "Flotila .201", Slug: "fleet-201"},
		Spec: Spec{Panels: []PanelSpec{
			{
				ID: "sluzby", Schema: SchemaStatus, Owner: "crew/lookout",
				Producer: "script/watch-services.sh", SLA: "30s", Span: 8,
				Actions: actions,
			},
			{
				ID: "zatizeni", Schema: SchemaMetric, Owner: "crew/lookout",
				Producer: "routine/load-check", SLA: "1h", Span: 4,
			},
		}},
	}
}

func callAction() PanelAction {
	return PanelAction{ID: "restart-api", Kind: ActionCall, Label: "Restart API", Routine: "restart-api"}
}

func TestValidate_Actions_AcceptsTheVocabulary(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		action PanelAction
	}{
		{"a bare call", callAction()},
		{"a call with a confirm step", PanelAction{
			ID: "drain", Kind: ActionCall, Label: "Drain", Style: ActionStyleDanger,
			Routine: "drain-node",
			Confirm: &PanelActionConfirm{Title: "Drain this node?", Body: "Running work is rescheduled."},
		}},
		{"a call with inputs and fixed params", PanelAction{
			ID: "scale", Kind: ActionCall, Label: "Scale", Routine: "scale-service",
			Params: map[string]any{"cluster": "prod"},
			Inputs: []PanelInput{
				{Name: "replicas", Type: "number", Required: true, Default: "2"},
				{Name: "tier", Type: "select", Options: []string{"web", "worker"}, Default: "web"},
				{Name: "note", Type: "textarea"},
			},
		}},
		{"a link to an internal entity", PanelAction{
			ID: "open-issue", Kind: ActionLink, Label: "Open the issue",
			Ref: &PanelEntityRef{Kind: "issue", ID: "ENG-15"},
		}},
		{"a toggle over a panel on this page", PanelAction{
			ID: "show-load", Kind: ActionToggle, Label: "Show load", Target: []string{"zatizeni"},
		}},
		{"a custom action with fixed params only", PanelAction{
			ID: "copy-ids", Kind: ActionCustom, Label: "Copy ids", Params: map[string]any{"field": "name"},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := actionsDoc(tc.action).Validate(); err != nil {
				t.Fatalf("refused a valid action: %v", err)
			}
		})
	}
}

// TestValidate_Actions_Refusals is the allow-list's negative half. An entry the
// validator does not fully understand must never be stored, because storing it
// is what would make §8 rule 4 aspirational.
func TestValidate_Actions_Refusals(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		actions []PanelAction
		want    string
	}{
		{
			// The headline rule: an undeclared kind is a hard refusal.
			name:    "an unknown kind",
			actions: []PanelAction{{ID: "nuke", Kind: "run", Label: "Run"}},
			want:    "the set is closed",
		},
		{
			name:    "no kind at all",
			actions: []PanelAction{{ID: "nuke", Label: "Run", Routine: "restart-api"}},
			want:    "declares no kind",
		},
		{
			name:    "an id that is not a slug",
			actions: []PanelAction{{ID: "Restart API!", Kind: ActionCall, Label: "Go", Routine: "restart-api"}},
			want:    "is not a slug",
		},
		{
			name:    "no label",
			actions: []PanelAction{{ID: "restart-api", Kind: ActionCall, Label: "  ", Routine: "restart-api"}},
			want:    "declares no label",
		},
		{
			name:    "a style outside the triad",
			actions: []PanelAction{{ID: "restart-api", Kind: ActionCall, Label: "Go", Style: "warning", Routine: "restart-api"}},
			want:    "default, primary, danger",
		},
		{
			name:    "a call naming no routine",
			actions: []PanelAction{{ID: "restart-api", Kind: ActionCall, Label: "Go"}},
			want:    "names no routine",
		},
		{
			name:    "a call whose routine is not a slug",
			actions: []PanelAction{{ID: "restart-api", Kind: ActionCall, Label: "Go", Routine: "../../etc/passwd"}},
			want:    "not a routine slug",
		},
		{
			// §8 rule 3, on the one string field left that could hold a URL.
			name: "a link carrying a URL in place of an id",
			actions: []PanelAction{{ID: "phone-home", Kind: ActionLink, Label: "Details",
				Ref: &PanelEntityRef{Kind: "issue", ID: "https://evil.example/x"}}},
			want: "never a URL",
		},
		{
			name: "a link to an entity kind outside the set",
			actions: []PanelAction{{ID: "phone-home", Kind: ActionLink, Label: "Details",
				Ref: &PanelEntityRef{Kind: "url", ID: "abc"}}},
			want: "issue, run, page, agent",
		},
		{
			name:    "a link with no ref",
			actions: []PanelAction{{ID: "phone-home", Kind: ActionLink, Label: "Details"}},
			want:    "carries no ref",
		},
		{
			// The cross-kind smuggle: a link that also names a routine would be
			// an allow-list entry the dispatcher must then be trusted to ignore.
			name: "a link that also names a routine",
			actions: []PanelAction{{ID: "phone-home", Kind: ActionLink, Label: "Details",
				Ref: &PanelEntityRef{Kind: "issue", ID: "ENG-15"}, Routine: "restart-api"}},
			want: "is a link and also names routine",
		},
		{
			name:    "a toggle targeting a panel that is not on the page",
			actions: []PanelAction{{ID: "show-x", Kind: ActionToggle, Label: "Show", Target: []string{"nope"}}},
			want:    "is not on this page",
		},
		{
			name:    "a toggle with no target",
			actions: []PanelAction{{ID: "show-x", Kind: ActionToggle, Label: "Show"}},
			want:    "names no target panel",
		},
		{
			name: "a custom action reaching for a routine",
			actions: []PanelAction{{ID: "copy", Kind: ActionCustom, Label: "Copy",
				Routine: "restart-api"}},
			want: "registered in our own client",
		},
		{
			name: "two actions sharing an id",
			actions: []PanelAction{
				{ID: "restart-api", Kind: ActionCall, Label: "A", Routine: "restart-api"},
				{ID: "restart-api", Kind: ActionCall, Label: "B", Routine: "load-check"},
			},
			want: "ids are unique within the page",
		},
		{
			name: "a confirm step with no body",
			actions: []PanelAction{{ID: "drain", Kind: ActionCall, Label: "Drain", Routine: "drain-node",
				Confirm: &PanelActionConfirm{Title: "Sure?"}}},
			want: "confirm step with no body",
		},
		{
			name: "an input name that is not an input name",
			actions: []PanelAction{{ID: "scale", Kind: ActionCall, Label: "Scale", Routine: "scale-service",
				Inputs: []PanelInput{{Name: "replica-count"}}}},
			want: "is not an input name",
		},
		{
			name: "an input colliding with a fixed param",
			actions: []PanelAction{{ID: "scale", Kind: ActionCall, Label: "Scale", Routine: "scale-service",
				Params: map[string]any{"cluster": "prod"},
				Inputs: []PanelInput{{Name: "cluster"}}}},
			want: "both a fixed param and a collected input",
		},
		{
			// §1: a page holds no credentials, so no field on it collects one.
			name: "an input collecting a secret",
			actions: []PanelAction{{ID: "deploy", Kind: ActionCall, Label: "Deploy", Routine: "deploy",
				Inputs: []PanelInput{{Name: "token", Type: "secret"}}}},
			want: "a page holds no credentials",
		},
		{
			name: "a select with no options",
			actions: []PanelAction{{ID: "scale", Kind: ActionCall, Label: "Scale", Routine: "scale-service",
				Inputs: []PanelInput{{Name: "tier", Type: "select"}}}},
			want: "select with no options",
		},
		{
			name: "a default that is not one of the options",
			actions: []PanelAction{{ID: "scale", Kind: ActionCall, Label: "Scale", Routine: "scale-service",
				Inputs: []PanelInput{{Name: "tier", Type: "select", Options: []string{"web"}, Default: "worker"}}}},
			want: "not one of its options",
		},
		{
			name: "options on something that is not a select",
			actions: []PanelAction{{ID: "scale", Kind: ActionCall, Label: "Scale", Routine: "scale-service",
				Inputs: []PanelInput{{Name: "tier", Type: "text", Options: []string{"web"}}}}},
			want: "only a select has options",
		},
		{
			name:    "more actions than a panel may carry",
			actions: manyActions(MaxActionsPerPanel + 1),
			want:    "the cap is",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := actionsDoc(tc.actions...).Validate()
			if err == nil {
				t.Fatalf("accepted %s; the stored spec is the allow-list a click resolves against", tc.name)
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("error is %T, want *ValidationError", err)
			}
			if ve.Code != CodeInvalidSpec {
				t.Errorf("code = %q, want %q", ve.Code, CodeInvalidSpec)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("message %q does not mention %q — refusing for the right reason is the point", err, tc.want)
			}
		})
	}
}

func manyActions(n int) []PanelAction {
	out := make([]PanelAction, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, PanelAction{
			ID: "a" + string(rune('a'+i)), Kind: ActionCall, Label: "Go", Routine: "restart-api",
		})
	}
	return out
}

// TestValidate_Actions_NarrativePanelMayNotAct is §8 rule 9 and §12's staging:
// a panel that renders agent-written prose and can also trigger an action is
// the lethal-trifecta shape, and v1 does not ship it.
func TestValidate_Actions_NarrativePanelMayNotAct(t *testing.T) {
	t.Parallel()

	doc := actionsDoc()
	doc.Spec.Panels[0].Schema = SchemaNarrative
	doc.Spec.Panels[0].Actions = []PanelAction{callAction()}

	err := doc.Validate()
	if err == nil {
		t.Fatal("a narrative panel was allowed to declare an action")
	}
	if !strings.Contains(err.Error(), "§8 rule 9") {
		t.Errorf("message %q does not name the rule it is enforcing", err)
	}

	// The same panel without actions is fine — the refusal is about the pair,
	// not about narrative panels.
	doc.Spec.Panels[0].Actions = nil
	if err := doc.Validate(); err != nil {
		t.Fatalf("a narrative panel with no actions was refused: %v", err)
	}
}

// TestValidate_Actions_ToggleMayTargetItsOwnPanel guards against an
// over-eager "target must be a DIFFERENT panel" reading: collapsing the panel
// you clicked from is a legitimate toggle.
func TestValidate_Actions_ToggleMayTargetItsOwnPanel(t *testing.T) {
	t.Parallel()

	doc := actionsDoc(PanelAction{ID: "collapse", Kind: ActionToggle, Label: "Collapse", Target: []string{"sluzby"}})
	if err := doc.Validate(); err != nil {
		t.Fatalf("a toggle over its own panel was refused: %v", err)
	}
}

// TestParseDocument_ActionsRoundTripFromYAML proves the vocabulary is reachable
// from the authoring format, not only from Go. KnownFields(true) is on, so a
// field this test spells differently from spec.go would fail here.
func TestParseDocument_ActionsRoundTripFromYAML(t *testing.T) {
	t.Parallel()

	const doc = `
apiVersion: crewship/v1
kind: Page
metadata:
  name: Flotila .201
  slug: fleet-201
spec:
  panels:
    - id: sluzby
      schema: status.v1
      owner: crew/lookout
      producer: script/watch-services.sh
      sla: 30s
      actions:
        - id: restart-api
          kind: call
          label: Restart API
          style: danger
          routine: restart-api
          confirm:
            title: Restart the API?
            body: In-flight requests are dropped.
            confirm_label: Restart
          params:
            cluster: prod
          inputs:
            - name: reason
              label: Why
              type: text
              required: true
        - id: open-issue
          kind: link
          label: Open the incident
          ref:
            kind: issue
            id: ENG-15
`
	parsed, err := ParseDocument([]byte(doc))
	if err != nil {
		t.Fatalf("the authored form was refused: %v", err)
	}
	actions := parsed.Spec.Panels[0].Actions
	if len(actions) != 2 {
		t.Fatalf("got %d actions, want 2", len(actions))
	}
	a, ok := parsed.Spec.Panels[0].FindAction("restart-api")
	if !ok {
		t.Fatal("FindAction did not find the action the document declares")
	}
	if a.Kind != ActionCall || a.Routine != "restart-api" {
		t.Errorf("action = (%q, routine %q), want (call, restart-api)", a.Kind, a.Routine)
	}
	if a.Confirm == nil || a.Confirm.ConfirmLabel != "Restart" {
		t.Errorf("confirm did not survive the parse: %+v", a.Confirm)
	}
	if got := a.Params["cluster"]; got != "prod" {
		t.Errorf("params[cluster] = %v, want prod", got)
	}
	if _, ok := parsed.Spec.Panels[0].FindAction("nope"); ok {
		t.Error("FindAction found an action the page does not declare")
	}
}

// ── ResolveInputs — the dispatch-time half ─────────────────────────────────

func scaleAction() PanelAction {
	return PanelAction{
		ID: "scale", Kind: ActionCall, Label: "Scale", Routine: "scale-service",
		Params: map[string]any{"cluster": "prod"},
		Inputs: []PanelInput{
			{Name: "replicas", Type: "number", Required: true},
			{Name: "tier", Type: "select", Options: []string{"web", "worker"}, Default: "web"},
			{Name: "drain", Type: "boolean"},
			{Name: "note", Type: "text"},
		},
	}
}

func TestResolveInputs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		action  PanelAction
		raw     map[string]any
		want    map[string]any
		wantErr string
	}{
		{
			name:   "coerces to the declared types and fills the declared default",
			action: scaleAction(),
			raw:    map[string]any{"replicas": "4", "drain": "true"},
			want:   map[string]any{"replicas": float64(4), "drain": true, "tier": "web", "cluster": "prod"},
		},
		{
			name:   "an omitted optional with no default stays absent",
			action: scaleAction(),
			raw:    map[string]any{"replicas": float64(1)},
			want:   map[string]any{"replicas": float64(1), "tier": "web", "cluster": "prod"},
		},
		{
			// §8b.2 in one assertion: the body has no field for a routine, so a
			// key called `routine` is simply an input the action never declared.
			name:    "a key the action did not declare is refused",
			action:  scaleAction(),
			raw:     map[string]any{"replicas": float64(1), "routine": "delete-everything"},
			wantErr: `declares no input named "routine"`,
		},
		{
			// Params are the author's. A caller reaching for one is told so,
			// rather than having it quietly overwritten.
			name:    "a fixed param is not the caller's to set",
			action:  scaleAction(),
			raw:     map[string]any{"replicas": float64(1), "cluster": "staging"},
			wantErr: "is not yours to set",
		},
		{
			name:    "a missing required input",
			action:  scaleAction(),
			raw:     map[string]any{},
			wantErr: `requires input "replicas"`,
		},
		{
			name:    "a select value outside its options",
			action:  scaleAction(),
			raw:     map[string]any{"replicas": float64(1), "tier": "database"},
			wantErr: "is not one of web, worker",
		},
		{
			name:    "a number that is not a number",
			action:  scaleAction(),
			raw:     map[string]any{"replicas": "four"},
			wantErr: `"four" is not a number`,
		},
		{
			name:    "an object where text was declared",
			action:  scaleAction(),
			raw:     map[string]any{"replicas": float64(1), "note": map[string]any{"a": 1}},
			wantErr: "expected text",
		},
		{
			// A link never reaches the server, so ResolveInputs refuses it here
			// as well as the handler refusing the route — the pure function is
			// what a future second caller would reach for.
			name:    "a link cannot be dispatched as a call",
			action:  PanelAction{ID: "open", Kind: ActionLink, Label: "Open", Ref: &PanelEntityRef{Kind: "issue", ID: "ENG-15"}},
			raw:     nil,
			wantErr: "only a call is dispatched",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := tc.action.ResolveInputs(tc.raw)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("accepted %v", tc.raw)
				}
				var ve *ValidationError
				if !errors.As(err, &ve) || ve.Code != CodeInvalidInput {
					t.Fatalf("error = %v (%T), want a *ValidationError with code %q", err, err, CodeInvalidInput)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("message %q does not mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("refused %v: %v", tc.raw, err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("resolved %v, want %v", got, tc.want)
			}
			for k, want := range tc.want {
				if got[k] != want {
					t.Errorf("resolved[%q] = %v (%T), want %v (%T)", k, got[k], got[k], want, want)
				}
			}
		})
	}
}

// TestResolveInputs_ParamsWinOverAnythingSupplied is the belt-and-braces half
// of the params rule. validateInputs already refuses a spec where an input and
// a param share a name; this proves that even a spec stored before that rule
// existed cannot let a caller's value reach the routine under a param's name.
func TestResolveInputs_ParamsWinOverAnythingSupplied(t *testing.T) {
	t.Parallel()

	// Constructed directly, bypassing Validate, precisely because Validate
	// refuses this shape.
	a := PanelAction{
		ID: "scale", Kind: ActionCall, Label: "Scale", Routine: "scale-service",
		Params: map[string]any{"cluster": "prod"},
		Inputs: []PanelInput{{Name: "cluster", Type: "text"}},
	}
	got, err := a.ResolveInputs(map[string]any{"cluster": "staging"})
	if err != nil {
		t.Fatalf("ResolveInputs: %v", err)
	}
	if got["cluster"] != "prod" {
		t.Errorf("cluster = %v, want prod — the author's fixed param must win", got["cluster"])
	}
}
