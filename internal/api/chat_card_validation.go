package api

import (
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/chatentities"
)

const maxCardInteger = int64(1<<53 - 1) // JSON numbers must round-trip through JavaScript.

var errInvalidCard = errors.New("invalid card projection")
var decisionFieldID = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)

func cardText(s string, min, max int) bool {
	n := utf8.RuneCountInString(s)
	return utf8.ValidString(s) && n >= min && n <= max
}
func cardTime(s string) bool { _, err := time.Parse(time.RFC3339Nano, s); return err == nil }
func cardActions(actions, allowed []string) bool {
	seen := map[string]bool{}
	for _, action := range actions {
		if seen[action] || !slices.Contains(allowed, action) {
			return false
		}
		seen[action] = true
	}
	return seen["view"]
}

func validateE2Card(ref ChatCardResponse) error {
	if ref.Current != nil || ref.Entity.Deleted || !cardText(ref.EventID, 1, 200) || !cardText(ref.OperationID, 0, 200) ||
		!cardText(ref.Entity.ID, 1, 200) || !cardText(ref.Entity.WorkspaceID, 1, 200) || !cardText(ref.Entity.Identifier, 0, 200) ||
		!cardText(ref.Anchor.RunID, 1, 200) || !cardText(ref.Anchor.TriggerMessageID, 1, 200) ||
		!cardText(ref.Provenance.Actor.ID, 1, 200) || !slices.Contains([]string{"agent", "human", "system"}, ref.Provenance.Actor.Type) ||
		!cardText(ref.Verb, 1, 64) || ref.Anchor.RunStatus != nil && !cardText(*ref.Anchor.RunStatus, 1, 64) ||
		!cardTime(ref.Provenance.At) || ref.RevisionAtAction.Revision < 0 || ref.RevisionAtAction.AggregateRevision < 0 ||
		ref.RevisionAtAction.Revision > maxCardInteger || ref.RevisionAtAction.AggregateRevision > maxCardInteger {
		return errInvalidCard
	}
	switch ref.Kind {
	case "proposal":
		if ref.Proposal == nil || ref.Decision != nil || ref.Provenance.Actor.Type != "agent" {
			return errInvalidCard
		}
		return validateProposalCard(*ref.Proposal)
	case "decision":
		if ref.Decision == nil || ref.Proposal != nil || ref.Decision.Source.ID != ref.Entity.ID {
			return errInvalidCard
		}
		return validateDecisionCard(*ref.Decision)
	default:
		return errInvalidCard
	}
}

func validateProposalCard(p chatentities.ProposalCard) error {
	if p.SchemaVersion != 1 || p.Version < 1 || p.Version > maxCardInteger || !slices.Contains([]string{"draft", "accepted", "discarded", "expired"}, p.Status) ||
		!cardText(p.Title, 1, 240) || !cardTime(p.ExpiresAt) || len(p.Items) < 1 || len(p.Items) > chatentities.MaxProposalItems {
		return errInvalidCard
	}
	allowed := []string{"view"}
	if p.Status == "draft" {
		allowed = append(allowed, "edit", "confirm", "discard")
	}
	if !cardActions(p.AllowedActions, allowed) {
		return errInvalidCard
	}
	seen, selected := map[string]bool{}, 0
	for _, item := range p.Items {
		if seen[item.ID] || !cardText(item.ID, 1, 200) || !cardText(item.CrewID, 1, 200) || !cardText(item.Title, 1, 240) ||
			!cardText(item.Description, 0, 10000) || !slices.Contains([]string{"none", "urgent", "high", "medium", "low"}, item.Priority) ||
			item.ProjectID != nil && !cardText(*item.ProjectID, 1, 200) || item.AssigneeID != nil && !cardText(*item.AssigneeID, 1, 200) {
			return errInvalidCard
		}
		seen[item.ID] = true
		if item.Selected {
			selected++
		}
	}
	if p.Status != "accepted" {
		if p.Outcome != nil {
			return errInvalidCard
		}
		return nil
	}
	o := p.Outcome
	if o == nil || selected == 0 || len(o.Issues) != selected || o.ReviewedVersion < 1 || o.ReviewedVersion > p.Version ||
		!cardText(o.ConfirmedBy, 1, 200) || !cardTime(o.ConfirmedAt) {
		return errInvalidCard
	}
	seen = map[string]bool{}
	for _, issue := range o.Issues {
		if seen[issue.ID] || !cardText(issue.ID, 1, 200) || !cardText(issue.Identifier, 1, 200) {
			return errInvalidCard
		}
		seen[issue.ID] = true
	}
	return nil
}

func validateDecisionCard(d chatentities.DecisionCard) error {
	if d.SchemaVersion != 1 || !cardText(d.Title, 1, 240) || !cardText(d.Source.ID, 1, 200) || !cardText(d.Source.Version, 1, 128) ||
		!slices.Contains([]string{"escalation", "waitpoint", "run_approval"}, d.Source.Type) ||
		!slices.Contains([]string{"information", "workflow", "whole_run_approval"}, d.DecisionType) ||
		(d.Source.Type == "run_approval") != (d.DecisionType == "whole_run_approval") ||
		!slices.Contains([]string{"pending", "answered", "approved", "rejected", "expired", "cancelled"}, d.Status) ||
		d.Fields == nil || len(d.Fields) > 20 || d.ExpiresAt != nil && !cardTime(*d.ExpiresAt) {
		return errInvalidCard
	}
	approval := d.DecisionType == "whole_run_approval"
	if approval && d.Status == "answered" || !approval && (d.Status == "approved" || d.Status == "rejected") {
		return errInvalidCard
	}
	allowed := []string{"view"}
	if d.Status == "pending" {
		if d.DecisionType == "whole_run_approval" {
			allowed = append(allowed, "approve", "reject")
		} else {
			allowed = append(allowed, "answer")
		}
	}
	if !cardActions(d.AllowedActions, allowed) {
		return errInvalidCard
	}
	fields := map[string]chatentities.DecisionField{}
	for _, f := range d.Fields {
		if _, duplicate := fields[f.ID]; duplicate || !decisionFieldID.MatchString(f.ID) || !cardText(f.Label, 1, 240) ||
			!slices.Contains([]string{"text", "textarea", "choice", "boolean", "number", "datetime"}, f.Type) {
			return errInvalidCard
		}
		if f.Type == "choice" {
			if len(f.Options) < 1 || len(f.Options) > 20 {
				return errInvalidCard
			}
			seen := map[string]bool{}
			for _, option := range f.Options {
				if seen[option.Value] || !cardText(option.Value, 1, 200) || !cardText(option.Label, 1, 240) {
					return errInvalidCard
				}
				seen[option.Value] = true
			}
		} else if len(f.Options) != 0 {
			return errInvalidCard
		}
		fields[f.ID] = f
	}
	if d.Status == "pending" || d.Status == "expired" || d.Status == "cancelled" {
		if d.Resolution != nil {
			return errInvalidCard
		}
		return nil
	}
	r := d.Resolution
	if r == nil || !cardText(r.AnsweredBy, 1, 200) || !cardTime(r.AnsweredAt) || r.Values == nil {
		return errInvalidCard
	}
	for id, raw := range r.Values {
		f, exists := fields[id]
		var value any
		if !exists || json.Unmarshal(raw, &value) != nil {
			return errInvalidCard
		}
		switch f.Type {
		case "boolean":
			if _, ok := value.(bool); !ok {
				return errInvalidCard
			}
		case "number":
			if _, ok := value.(float64); !ok {
				return errInvalidCard
			}
		default:
			s, ok := value.(string)
			if !ok || !cardText(s, 0, 10000) || f.Required && s == "" || f.Type == "datetime" && !cardTime(s) {
				return errInvalidCard
			}
			if f.Type == "choice" && !slices.ContainsFunc(f.Options, func(o chatentities.DecisionOption) bool { return o.Value == s }) {
				return errInvalidCard
			}
		}
	}
	if d.Status == "answered" {
		for id, f := range fields {
			if _, exists := r.Values[id]; f.Required && !exists {
				return errInvalidCard
			}
		}
	}
	return nil
}
