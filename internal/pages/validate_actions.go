package pages

// Actions — validation (docs/prd/pages.md §8b.1, and §8 rules 3, 4, 5, 9).
//
// spec.go declares the vocabulary. This file is the half that makes it mean
// something: an action that does not validate is never stored, and the dispatch
// endpoint (internal/api/pages_actions.go) resolves a click against the STORED
// spec. So every rule here is load-bearing at click time, not at authoring time
// — the stored spec is the allow-list, and this is what gets to be in it.
//
// The refusals are hard, never warnings. §8 rule 4 is "actions come from the
// page's declared allow-list only"; an allow-list that admits an entry it does
// not understand — an unknown kind, a `call` naming nothing, a `link` carrying
// something URL-shaped — is not an allow-list. A page whose author typed
// `kind: run` gets a 400 naming the four kinds, which is a fixable message; a
// page that saved it and rendered a dead button is not.
//
// Two rules here are not in §8b.1 and are argued for rather than assumed:
//
//  1. A narrative.v1 panel may not declare actions. §12 stages it exactly that
//     way ("narrative.v1, text only, no actions" in v1; "narrative.v1 actions,
//     with the full §8 rule set" in v1.1), and §8 rule 9 says why: a panel that
//     both displays untrusted agent-written prose and can trigger an action is
//     the lethal-trifecta shape. The gate is here rather than remembered.
//
//  2. An input may not be named after a fixed param, and no input type collects
//     a secret. Params are author-controlled and inputs are user-supplied
//     (§8b.1); a collision makes it ambiguous which one the routine receives,
//     and the safe resolution — params win — leaves a form field that silently
//     does nothing. A page holds no credentials at all (§1), so a field asking
//     for one is refused rather than rendered.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Structural limits on the action surface. Small on purpose: a panel is a
// glanceable tile, and a tile with a dozen buttons is a form nobody read. These
// are shape constants like MaxPanelsPerPage, not tunables.
const (
	// MaxActionsPerPanel — beyond this the panel is a toolbar.
	MaxActionsPerPanel = 6
	// MaxInputsPerAction — an action needing more parameters than this wants a
	// routine with a form, not a button on a dashboard.
	MaxInputsPerAction = 8
	// MaxParamsPerAction bounds the fixed, author-supplied payload.
	MaxParamsPerAction = 24
	// MaxSelectOptions bounds a select input's declared choices.
	MaxSelectOptions = 32
	// MaxActionLabelRunes / MaxConfirmTextRunes bound what host chrome draws.
	// The confirm dialog is drawn by the host (§8 rule 5), so its text is a
	// budget the host owns rather than something panel content can grow.
	MaxActionLabelRunes = 80
	MaxConfirmTextRunes = 400
	// MaxInputValueBytes bounds one collected input at dispatch time. The whole
	// body is bounded too (the handler's cap); this bounds a single field so a
	// four-field form cannot smuggle a payload through one of them.
	MaxInputValueBytes = 4096
)

// CodeInvalidInput — a dispatch request's collected inputs do not satisfy the
// action's declaration. Distinct from CodeInvalidSpec because they are opposite
// faults with opposite fixes: the spec is the author's, the inputs are the
// caller's, and a click that says "your page is malformed" when the caller
// simply left a field blank sends the wrong person to the wrong file.
const CodeInvalidInput ErrorCode = "invalid_input"

// inputNameRE is the shape of a collected parameter's name. It is what the
// routine's own `{{ inputs.<name> }}` reference looks like, so a dot or a dash
// is refused here rather than rendering to nothing three layers down.
var inputNameRE = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,63}$`)

// entityRefIDRE is the shape of an internal entity id: an issue key (ENG-15), a
// run id (crun…), a page or agent slug. No slash, no colon, no whitespace —
// which is exactly what makes `https://…` unrepresentable. §8 rule 3 removes
// the URL field; this stops the remaining field from becoming one.
var entityRefIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// knownActionKinds is closed. A new kind is a server release — the same rule
// PanelSchema lives under, for the same reason (§2.4b: Spotify's HubFramework
// went fully generic too early and was deprecated for it).
var knownActionKinds = map[PanelActionKind]bool{
	ActionCall:   true,
	ActionLink:   true,
	ActionToggle: true,
	ActionCustom: true,
}

// Known reports whether k is a member of the closed set.
func (k PanelActionKind) Known() bool { return knownActionKinds[k] }

func (k PanelActionKind) String() string { return string(k) }

var knownActionStyles = map[PanelActionStyle]bool{
	ActionStyleDefault: true,
	ActionStylePrimary: true,
	ActionStyleDanger:  true,
}

// knownEntityRefKinds is the set §8 rule 3 names verbatim — "an internal
// Crewship entity by id (issue, run, page, agent)" — and nothing else. The
// renderer builds the address from the pair; adding a kind is a renderer change
// and therefore a server release, not a spec field.
var knownEntityRefKinds = map[string]bool{
	"issue": true,
	"run":   true,
	"page":  true,
	"agent": true,
}

// knownInputTypes maps onto the field switch SlashActionModal already renders
// (§8b.4), so the surface has one field switch rather than two.
//
// There is deliberately no `secret` member, though the slash surface has one: a
// page holds no credentials (§1), and an action that collected one would put a
// credential in a request body whose only job is to name an allow-listed
// operation.
var knownInputTypes = map[string]bool{
	"text":     true,
	"textarea": true,
	"number":   true,
	"boolean":  true,
	"select":   true,
}

// DefaultInputType is what an input that declares no type gets.
const DefaultInputType = "text"

// validatePageActions checks every action on every panel of one page.
//
// It runs over the whole page rather than per panel because two of its rules
// are page-scoped: action ids are unique within the PAGE (§8b.1), and a toggle
// may only target a panel that exists on it.
func validatePageActions(panels []PanelSpec) error {
	// Panel ids are already known unique by the time this runs (Document.Validate
	// checks that first), so this set is exactly the toggle target vocabulary.
	panelIDs := make(map[string]bool, len(panels))
	for i := range panels {
		panelIDs[panels[i].ID] = true
	}

	seenActionID := make(map[string]string, len(panels)) // action id → panel that declared it
	for i := range panels {
		p := &panels[i]
		if len(p.Actions) == 0 {
			continue
		}
		// §12 v1 / §8 rule 9 — see the file comment. The refusal names the
		// staging rather than pretending the field does not exist, because an
		// author who wrote it read §8b and is not wrong about where this goes.
		if p.Schema == SchemaNarrative {
			return newError(CodeInvalidSpec, p.Schema,
				"panel %q is narrative.v1 and declares %d action(s); a panel that renders agent-written prose "+
					"and can also trigger an action is the shape §8 rule 9 refuses, and §12 stages narrative "+
					"actions for v1.1 behind the full rule set", p.ID, len(p.Actions))
		}
		if len(p.Actions) > MaxActionsPerPanel {
			return newError(CodeInvalidSpec, p.Schema,
				"panel %q declares %d actions; the cap is %d — a panel with more buttons than that is a toolbar",
				p.ID, len(p.Actions), MaxActionsPerPanel)
		}
		for j := range p.Actions {
			a := &p.Actions[j]
			if !slugRE.MatchString(a.ID) {
				return newError(CodeInvalidSpec, p.Schema,
					"panel %q action %d: id %q is not a slug; it is what a click posts and it goes in a URL path",
					p.ID, j, a.ID)
			}
			if owner, dup := seenActionID[a.ID]; dup {
				return newError(CodeInvalidSpec, p.Schema,
					"action id %q is declared on panel %q and again on panel %q; ids are unique within the page "+
						"(§8b.1) because the dispatch endpoint resolves a click by id", a.ID, owner, p.ID)
			}
			seenActionID[a.ID] = p.ID

			if err := validateAction(p, a); err != nil {
				return err
			}
			if a.Kind == ActionToggle {
				if err := validateToggleTargets(p, a, panelIDs); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// validateAction checks one action's own shape.
func validateAction(p *PanelSpec, a *PanelAction) error {
	if strings.TrimSpace(a.Label) == "" {
		return newError(CodeInvalidSpec, p.Schema,
			"panel %q action %q declares no label; a button with no words on it is not operable", p.ID, a.ID)
	}
	if n := len([]rune(a.Label)); n > MaxActionLabelRunes {
		return newError(CodeInvalidSpec, p.Schema,
			"panel %q action %q: label is %d characters; the cap is %d", p.ID, a.ID, n, MaxActionLabelRunes)
	}
	if a.Style != "" && !knownActionStyles[a.Style] {
		return newError(CodeInvalidSpec, p.Schema,
			"panel %q action %q declares style %q; the set is default, primary, danger", p.ID, a.ID, a.Style)
	}
	if a.Kind == "" {
		return newError(CodeInvalidSpec, p.Schema,
			"panel %q action %q declares no kind; the set is call, link, toggle, custom (§8b.1) and there is no default — "+
				"a button whose kind was guessed is a button that does something nobody declared", p.ID, a.ID)
	}
	if !a.Kind.Known() {
		return newError(CodeInvalidSpec, p.Schema,
			"panel %q action %q declares kind %q; the set is closed: call, link, toggle, custom. "+
				"A new kind is a server release, not a spec field", p.ID, a.ID, a.Kind)
	}
	if err := validateConfirm(p, a); err != nil {
		return err
	}
	if len(a.Params) > MaxParamsPerAction {
		return newError(CodeInvalidSpec, p.Schema,
			"panel %q action %q declares %d params; the cap is %d", p.ID, a.ID, len(a.Params), MaxParamsPerAction)
	}

	switch a.Kind {
	case ActionCall:
		return validateCallAction(p, a)
	case ActionLink:
		return validateLinkAction(p, a)
	case ActionToggle:
		return validateToggleAction(p, a)
	case ActionCustom:
		return validateCustomAction(p, a)
	}
	// Unreachable: Known() above is the same closed set as this switch.
	return newError(CodeInvalidSpec, p.Schema,
		"panel %q action %q declares kind %q, which validated as known but has no rules; refusing rather than storing it",
		p.ID, a.ID, a.Kind)
}

func validateConfirm(p *PanelSpec, a *PanelAction) error {
	if a.Confirm == nil {
		// §8 rule 7: friction is calibrated to blast radius, so a read-only or
		// reversible action declaring no confirm is correct, not an omission.
		return nil
	}
	if strings.TrimSpace(a.Confirm.Title) == "" || strings.TrimSpace(a.Confirm.Body) == "" {
		return newError(CodeInvalidSpec, p.Schema,
			"panel %q action %q declares a confirm step with no %s; host chrome draws this dialog (§8 rule 5) "+
				"and cannot draw an empty one", p.ID, a.ID, confirmMissingField(a.Confirm))
	}
	for label, text := range map[string]string{
		"title": a.Confirm.Title, "body": a.Confirm.Body,
		"confirm_label": a.Confirm.ConfirmLabel, "cancel_label": a.Confirm.CancelLabel,
	} {
		if n := len([]rune(text)); n > MaxConfirmTextRunes {
			return newError(CodeInvalidSpec, p.Schema,
				"panel %q action %q: confirm %s is %d characters; the cap is %d",
				p.ID, a.ID, label, n, MaxConfirmTextRunes)
		}
	}
	return nil
}

func confirmMissingField(c *PanelActionConfirm) string {
	if strings.TrimSpace(c.Title) == "" {
		return "title"
	}
	return "body"
}

// validateCallAction — the only kind that reaches the server.
//
// The routine is named HERE and nowhere else. §8b.2: the wire format has no
// field for one, so this string is the whole allow-list, and a `call` that
// names nothing is an allow-list entry pointing at nothing.
func validateCallAction(p *PanelSpec, a *PanelAction) error {
	routine := strings.TrimSpace(a.Routine)
	if routine == "" {
		return newError(CodeInvalidSpec, p.Schema,
			"panel %q action %q is a call and names no routine; the routine is read from the spec at dispatch time "+
				"and never from the request (§8b.2), so an unnamed one can never be resolved", p.ID, a.ID)
	}
	if !slugRE.MatchString(routine) {
		return newError(CodeInvalidSpec, p.Schema,
			"panel %q action %q names routine %q, which is not a routine slug", p.ID, a.ID, a.Routine)
	}
	if a.Ref != nil {
		return newError(CodeInvalidSpec, p.Schema,
			"panel %q action %q is a call and also carries a ref; a ref belongs to a link", p.ID, a.ID)
	}
	if len(a.Target) > 0 {
		return newError(CodeInvalidSpec, p.Schema,
			"panel %q action %q is a call and also carries a target; a target belongs to a toggle", p.ID, a.ID)
	}
	return validateInputs(p, a)
}

// validateLinkAction — §8 rule 3, enforced twice.
//
// Once in the schema: PanelAction has no URL field and PanelEntityRef has no
// URL field, so there is nowhere to put one. And once here, on the id, because
// the remaining string field is where a URL would otherwise be smuggled. Slack
// AI's private-channel exfiltration was a rendered link and CamoLeak proved a
// trusted first-party proxy is not a defence, so the renderer builds every
// address itself from a (kind, id) pair it recognises.
func validateLinkAction(p *PanelSpec, a *PanelAction) error {
	if a.Ref == nil {
		return newError(CodeInvalidSpec, p.Schema,
			"panel %q action %q is a link and carries no ref; a link points at an internal entity by id "+
				"and the renderer builds the address (§8 rule 3)", p.ID, a.ID)
	}
	kind := strings.TrimSpace(a.Ref.Kind)
	if !knownEntityRefKinds[kind] {
		return newError(CodeInvalidSpec, p.Schema,
			"panel %q action %q links to entity kind %q; the set is issue, run, page, agent (§8 rule 3). "+
				"There is no URL kind and there will not be one", p.ID, a.ID, a.Ref.Kind)
	}
	id := strings.TrimSpace(a.Ref.ID)
	if !entityRefIDRE.MatchString(id) {
		return newError(CodeInvalidSpec, p.Schema,
			"panel %q action %q links to %s/%q, which is not an entity id; a link carries an id, never a URL "+
				"(§8 rule 3 — Slack AI's leak was a rendered link)", p.ID, a.ID, kind, a.Ref.ID)
	}
	if strings.TrimSpace(a.Routine) != "" {
		return newError(CodeInvalidSpec, p.Schema,
			"panel %q action %q is a link and also names routine %q; a link never reaches the server, so the "+
				"routine would be dead text in the allow-list — and the dispatch endpoint refuses to run it "+
				"(§8b.2)", p.ID, a.ID, a.Routine)
	}
	if len(a.Inputs) > 0 || len(a.Params) > 0 || len(a.Target) > 0 {
		return newError(CodeInvalidSpec, p.Schema,
			"panel %q action %q is a link and carries inputs, params or a target; a link navigates and nothing else",
			p.ID, a.ID)
	}
	return nil
}

// validateToggleAction — local client state, never a request.
func validateToggleAction(p *PanelSpec, a *PanelAction) error {
	if len(a.Target) == 0 {
		return newError(CodeInvalidSpec, p.Schema,
			"panel %q action %q is a toggle and names no target panel; a toggle shows or hides panels and "+
				"nothing else", p.ID, a.ID)
	}
	if strings.TrimSpace(a.Routine) != "" || a.Ref != nil || len(a.Inputs) > 0 {
		return newError(CodeInvalidSpec, p.Schema,
			"panel %q action %q is a toggle and carries a routine, ref or inputs; a toggle is local client state "+
				"and never reaches the server", p.ID, a.ID)
	}
	return nil
}

// validateToggleTargets resolves the targets against the page. Deferred out of
// validateToggleAction because it needs the whole page, not one panel.
func validateToggleTargets(p *PanelSpec, a *PanelAction, panelIDs map[string]bool) error {
	seen := make(map[string]bool, len(a.Target))
	for _, t := range a.Target {
		if !panelIDs[t] {
			return newError(CodeInvalidSpec, p.Schema,
				"panel %q action %q toggles panel %q, which is not on this page; a toggle that targets nothing "+
					"renders as a button that does nothing", p.ID, a.ID, t)
		}
		if seen[t] {
			return newError(CodeInvalidSpec, p.Schema,
				"panel %q action %q lists panel %q twice as a toggle target", p.ID, a.ID, t)
		}
		seen[t] = true
	}
	return nil
}

// validateCustomAction — a handler WE registered in our own client at build
// time (§8b.1), resolved by the action id. It never names user-supplied code,
// which is why it costs nothing in safety and can exist from day one.
func validateCustomAction(p *PanelSpec, a *PanelAction) error {
	if strings.TrimSpace(a.Routine) != "" || a.Ref != nil || len(a.Target) > 0 || len(a.Inputs) > 0 {
		return newError(CodeInvalidSpec, p.Schema,
			"panel %q action %q is custom and carries a routine, ref, target or inputs; a custom action resolves "+
				"to a handler registered in our own client at build time and takes only fixed params", p.ID, a.ID)
	}
	return nil
}

// validateInputs checks the collected-parameter declaration.
func validateInputs(p *PanelSpec, a *PanelAction) error {
	if len(a.Inputs) > MaxInputsPerAction {
		return newError(CodeInvalidSpec, p.Schema,
			"panel %q action %q declares %d inputs; the cap is %d", p.ID, a.ID, len(a.Inputs), MaxInputsPerAction)
	}
	seen := make(map[string]bool, len(a.Inputs))
	for k := range a.Inputs {
		in := &a.Inputs[k]
		if !inputNameRE.MatchString(in.Name) {
			return newError(CodeInvalidSpec, p.Schema,
				"panel %q action %q input %d: name %q is not an input name (lower-case, digits and underscores); "+
					"it is what the routine reads as {{ inputs.%s }}", p.ID, a.ID, k, in.Name, in.Name)
		}
		if seen[in.Name] {
			return newError(CodeInvalidSpec, p.Schema,
				"panel %q action %q declares input %q twice", p.ID, a.ID, in.Name)
		}
		seen[in.Name] = true
		if _, clash := a.Params[in.Name]; clash {
			return newError(CodeInvalidSpec, p.Schema,
				"panel %q action %q declares %q as both a fixed param and a collected input; params are "+
					"author-controlled and win, so the field would be a form control that silently does nothing",
				p.ID, a.ID, in.Name)
		}
		if n := len([]rune(in.Label)); n > MaxActionLabelRunes {
			return newError(CodeInvalidSpec, p.Schema,
				"panel %q action %q input %q: label is %d characters; the cap is %d",
				p.ID, a.ID, in.Name, n, MaxActionLabelRunes)
		}

		typ := in.EffectiveType()
		if !knownInputTypes[typ] {
			hint := ""
			if typ == "secret" || typ == "password" {
				hint = " — a page holds no credentials (§1), so an action never collects one"
			}
			return newError(CodeInvalidSpec, p.Schema,
				"panel %q action %q input %q declares type %q; the set is text, textarea, number, boolean, select%s",
				p.ID, a.ID, in.Name, in.Type, hint)
		}
		if err := validateInputOptions(p, a, in, typ); err != nil {
			return err
		}
		if err := validateInputDefault(p, a, in, typ); err != nil {
			return err
		}
	}
	return nil
}

func validateInputOptions(p *PanelSpec, a *PanelAction, in *PanelInput, typ string) error {
	if typ != "select" {
		if len(in.Options) > 0 {
			return newError(CodeInvalidSpec, p.Schema,
				"panel %q action %q input %q is a %s and declares options; only a select has options",
				p.ID, a.ID, in.Name, typ)
		}
		return nil
	}
	if len(in.Options) == 0 {
		return newError(CodeInvalidSpec, p.Schema,
			"panel %q action %q input %q is a select with no options; there is nothing to pick",
			p.ID, a.ID, in.Name)
	}
	if len(in.Options) > MaxSelectOptions {
		return newError(CodeInvalidSpec, p.Schema,
			"panel %q action %q input %q declares %d options; the cap is %d",
			p.ID, a.ID, in.Name, len(in.Options), MaxSelectOptions)
	}
	seen := make(map[string]bool, len(in.Options))
	for _, o := range in.Options {
		if strings.TrimSpace(o) == "" {
			return newError(CodeInvalidSpec, p.Schema,
				"panel %q action %q input %q declares an empty option", p.ID, a.ID, in.Name)
		}
		if seen[o] {
			return newError(CodeInvalidSpec, p.Schema,
				"panel %q action %q input %q declares option %q twice", p.ID, a.ID, in.Name, o)
		}
		seen[o] = true
	}
	return nil
}

func validateInputDefault(p *PanelSpec, a *PanelAction, in *PanelInput, typ string) error {
	if in.Default == "" {
		return nil
	}
	switch typ {
	case "select":
		for _, o := range in.Options {
			if o == in.Default {
				return nil
			}
		}
		return newError(CodeInvalidSpec, p.Schema,
			"panel %q action %q input %q defaults to %q, which is not one of its options",
			p.ID, a.ID, in.Name, in.Default)
	case "number":
		if _, err := strconv.ParseFloat(in.Default, 64); err != nil {
			return newError(CodeInvalidSpec, p.Schema,
				"panel %q action %q input %q is a number and defaults to %q", p.ID, a.ID, in.Name, in.Default)
		}
	case "boolean":
		if in.Default != "true" && in.Default != "false" {
			return newError(CodeInvalidSpec, p.Schema,
				"panel %q action %q input %q is a boolean and defaults to %q; use true or false",
				p.ID, a.ID, in.Name, in.Default)
		}
	}
	return nil
}

// EffectiveType is the input's declared type, or the default when it declared
// none. One function so the validator and the renderer cannot disagree.
func (in PanelInput) EffectiveType() string {
	t := strings.ToLower(strings.TrimSpace(in.Type))
	if t == "" {
		return DefaultInputType
	}
	return t
}

// EffectiveStyle is the action's declared style, or "default".
func (a PanelAction) EffectiveStyle() PanelActionStyle {
	if a.Style == "" {
		return ActionStyleDefault
	}
	return a.Style
}

// FindAction returns the named action from this panel's DECLARED list.
//
// This is the lookup §8b.2 describes: a click posts an id, and the id is
// resolved against the stored spec. It is a method on PanelSpec, not a map
// built at dispatch time, so there is exactly one way to answer "does this
// panel offer that action" and it reads the same list the author wrote.
func (p PanelSpec) FindAction(id string) (*PanelAction, bool) {
	for i := range p.Actions {
		if p.Actions[i].ID == id {
			return &p.Actions[i], true
		}
	}
	return nil, false
}

// FindPanel returns the named panel from the document.
func (d *Document) FindPanel(id string) (*PanelSpec, bool) {
	for i := range d.Spec.Panels {
		if d.Spec.Panels[i].ID == id {
			return &d.Spec.Panels[i], true
		}
	}
	return nil, false
}

// ResolveInputs validates the caller's collected inputs against this action's
// declaration and returns the map the routine will actually receive.
//
// Three properties, in the order they matter:
//
//  1. An undeclared key is REFUSED, not dropped. §8b.2 makes the body "only the
//     collected inputs"; a body carrying anything else is either a client bug
//     or an attempt to reach a field the wire format does not have, and both
//     want to be told rather than absorbed. This is what makes a body naming a
//     routine a 400 instead of a silent no-op.
//  2. Fixed params are applied LAST and always win. They are author-controlled
//     (§8b.1); validateInputs already refuses a declaration where the two could
//     collide, so this is belt-and-braces against a spec stored before that
//     rule existed.
//  3. The result is the dispatch fingerprint. The handler hashes it into the
//     idempotency key, so "same key, same params" is decided on the RESOLVED
//     values — defaults filled, types coerced — rather than on whatever the
//     client happened to serialise.
func (a PanelAction) ResolveInputs(raw map[string]any) (map[string]any, error) {
	if a.Kind != ActionCall {
		return nil, &ValidationError{Code: CodeInvalidInput, Detail: fmt.Sprintf(
			"action %q is a %s, not a call; only a call takes inputs and only a call is dispatched", a.ID, a.Kind)}
	}
	declared := make(map[string]*PanelInput, len(a.Inputs))
	for i := range a.Inputs {
		declared[a.Inputs[i].Name] = &a.Inputs[i]
	}
	for k := range raw {
		if _, ok := declared[k]; ok {
			continue
		}
		if _, fixed := a.Params[k]; fixed {
			return nil, &ValidationError{Code: CodeInvalidInput, Detail: fmt.Sprintf(
				"action %q: %q is a fixed param declared on the page and is not yours to set", a.ID, k)}
		}
		return nil, &ValidationError{Code: CodeInvalidInput, Detail: fmt.Sprintf(
			"action %q declares no input named %q; the body carries the collected inputs and nothing else (§8b.2)",
			a.ID, k)}
	}

	out := make(map[string]any, len(a.Inputs)+len(a.Params))
	for i := range a.Inputs {
		in := &a.Inputs[i]
		v, present := raw[in.Name]
		if !present || isBlank(v) {
			if in.Default != "" {
				coerced, err := coerceInput(a.ID, in, in.Default)
				if err != nil {
					return nil, err
				}
				out[in.Name] = coerced
				continue
			}
			if in.Required {
				return nil, &ValidationError{Code: CodeInvalidInput, Detail: fmt.Sprintf(
					"action %q requires input %q", a.ID, in.Name)}
			}
			continue
		}
		coerced, err := coerceInput(a.ID, in, v)
		if err != nil {
			return nil, err
		}
		out[in.Name] = coerced
	}
	for k, v := range a.Params {
		out[k] = v
	}
	return out, nil
}

func isBlank(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(t) == ""
	default:
		return false
	}
}

// coerceInput turns one supplied value into the declared type, or refuses it.
//
// Coercion rather than strict typing because the value arrives from an HTML
// form as often as from JSON, and "42" from a number field is not a client bug.
// What it never does is widen the type: a select still has to name one of its
// options, and an object where a string was declared is refused rather than
// stringified into something the routine would then parse.
func coerceInput(actionID string, in *PanelInput, v any) (any, error) {
	refuse := func(format string, args ...any) (any, error) {
		return nil, &ValidationError{Code: CodeInvalidInput, Detail: fmt.Sprintf(
			"action %s input %q: "+format, append([]any{actionID, in.Name}, args...)...)}
	}
	switch in.EffectiveType() {
	case "boolean":
		switch t := v.(type) {
		case bool:
			return t, nil
		case string:
			b, err := strconv.ParseBool(strings.TrimSpace(t))
			if err != nil {
				return refuse("%q is not a boolean", t)
			}
			return b, nil
		}
		return refuse("expected a boolean, got %T", v)
	case "number":
		switch t := v.(type) {
		case float64:
			return t, nil
		case int:
			return float64(t), nil
		case json.Number:
			f, err := t.Float64()
			if err != nil {
				return refuse("%q is not a number", t.String())
			}
			return f, nil
		case string:
			f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
			if err != nil {
				return refuse("%q is not a number", t)
			}
			return f, nil
		}
		return refuse("expected a number, got %T", v)
	case "select":
		s, ok := v.(string)
		if !ok {
			return refuse("expected one of the declared options, got %T", v)
		}
		for _, o := range in.Options {
			if o == s {
				return s, nil
			}
		}
		return refuse("%q is not one of %s", s, strings.Join(in.Options, ", "))
	default: // text, textarea
		s, ok := v.(string)
		if !ok {
			return refuse("expected text, got %T", v)
		}
		if len(s) > MaxInputValueBytes {
			return refuse("%d bytes; the cap for one input is %d", len(s), MaxInputValueBytes)
		}
		return s, nil
	}
}
