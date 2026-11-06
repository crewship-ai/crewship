// Package credhostgate is the single evaluation of the approved-hosts
// precondition for host-bound credentials (#5045).
//
// A credential with a non-empty allowed_hosts list is "host-bound". Every path
// that hands such a credential's VALUE to a run or a service (container
// delivery, Keeper release, MCP and OAuth tokens, crew service env, the
// restricted runtime broker, routine steps) must ask this package first and
// treat any withhold, any error and any undeterminable policy as "do not
// deliver". Credentials with an empty list are never affected.
//
// Two shapes exist because the question differs:
//   - Withholds / WithholdsForCrew: the value reaches a container whose crew
//     egress allowlist is the boundary. Delivered only when the crew is
//     restricted and the allowlist covers every approved host.
//   - Bound / HostsPermit: the value goes to ONE known destination (a
//     server-side http step). Delivered only when that host is approved.
package credhostgate

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/crewship-ai/crewship/internal/credhosts"
	"github.com/crewship-ai/crewship/internal/egressallow"
	"github.com/crewship-ai/crewship/internal/egresspolicy"
)

// Querier is the read surface the gate needs; *sql.DB and *sql.Tx satisfy it.
type Querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Withhold describes one credential that must not be delivered.
type Withhold struct {
	CredentialID string
	Hosts        []string
	// Reason is the human-readable cause, without the trailing policy note.
	Reason string
}

// ParseStored parses a stored allowed_hosts column.
func ParseStored(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var hosts []string
	if err := json.Unmarshal([]byte(raw), &hosts); err != nil {
		return nil, err
	}
	return credhosts.NormalizeList(hosts)
}

type bound struct {
	id    string
	hosts []string
	bad   bool
}

func loadBound(ctx context.Context, q Querier, ids []string) ([]bound, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := q.QueryContext(ctx,
		`SELECT id, COALESCE(allowed_hosts, '[]') FROM credentials WHERE id IN (`+ph+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("load credential allowed hosts: %w", err)
	}
	var out []bound
	for rows.Next() {
		var id, raw string
		if err := rows.Scan(&id, &raw); err != nil {
			rows.Close()
			return nil, err
		}
		if strings.TrimSpace(raw) == "" || strings.TrimSpace(raw) == "[]" {
			continue
		}
		hosts, perr := ParseStored(raw)
		out = append(out, bound{id: id, hosts: hosts, bad: perr != nil})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Withholds returns, for the given credentials that are host-bound, the ones
// that must NOT be delivered to a run of agentID. A non-nil error means the
// answer is unknown: callers must withhold everything they asked about.
func Withholds(ctx context.Context, q Querier, agentID string, ids []string) (map[string]Withhold, error) {
	boundCreds, err := loadBound(ctx, q, ids)
	if err != nil || len(boundCreds) == 0 {
		return map[string]Withhold{}, err
	}
	crewRows, err := q.QueryContext(ctx, `SELECT COALESCE(crew_id, '') FROM agents WHERE id = ?`, agentID)
	if err != nil {
		return nil, fmt.Errorf("load agent crew: %w", err)
	}
	crewID := ""
	if crewRows.Next() {
		_ = crewRows.Scan(&crewID)
	}
	crewRows.Close()
	return evaluate(ctx, q, crewID, boundCreds), nil
}

// WithholdsForCrew is Withholds for a delivery keyed on a crew (service env).
func WithholdsForCrew(ctx context.Context, q Querier, crewID string, ids []string) (map[string]Withhold, error) {
	boundCreds, err := loadBound(ctx, q, ids)
	if err != nil || len(boundCreds) == 0 {
		return map[string]Withhold{}, err
	}
	return evaluate(ctx, q, crewID, boundCreds), nil
}

func evaluate(ctx context.Context, q Querier, crewID string, boundCreds []bound) map[string]Withhold {
	out := map[string]Withhold{}
	var pol egresspolicy.Effective
	var polErr error
	if crewID != "" {
		pol, polErr = egresspolicy.ResolveEffective(ctx, q, crewID)
	}
	for _, b := range boundCreds {
		reason := ""
		switch {
		case b.bad || len(b.hosts) == 0:
			reason = "its approved host list is unreadable"
		case crewID == "":
			reason = "the agent is not in a crew, so no egress allowlist applies to it"
		case polErr != nil:
			reason = "the crew's network policy could not be read"
		case !pol.Restricted:
			reason = "the crew has no egress allowlist (network mode is free)"
		default:
			var missing []string
			for _, h := range b.hosts {
				if !credhosts.Covered(pol.Allow, h) {
					missing = append(missing, h)
				}
			}
			if len(missing) > 0 {
				reason = "these approved hosts are not in the crew's effective egress allowlist: " + strings.Join(missing, ", ")
			}
		}
		if reason != "" {
			out[b.id] = Withhold{CredentialID: b.id, Hosts: b.hosts, Reason: reason}
		}
	}
	return out
}

// Deliverable is the fail-closed single-credential form used by paths that
// release one value: true only when the credential is unbound, or bound and
// its approved hosts are satisfied. Any error returns false.
func Deliverable(ctx context.Context, q Querier, agentID, credentialID string) (bool, string) {
	w, err := Withholds(ctx, q, agentID, []string{credentialID})
	if err != nil {
		return false, "its approved hosts could not be checked"
	}
	if n, drop := w[credentialID]; drop {
		return false, n.Reason
	}
	return true, ""
}

// DeliverableMany filters ids to those that may be delivered to agentID. On
// error nothing is deliverable.
func DeliverableMany(ctx context.Context, q Querier, agentID string, ids []string) map[string]bool {
	ok := map[string]bool{}
	w, err := Withholds(ctx, q, agentID, ids)
	if err != nil {
		return ok
	}
	for _, id := range ids {
		if _, drop := w[id]; !drop {
			ok[id] = true
		}
	}
	return ok
}

// DeliverableManyForCrew is DeliverableMany keyed on a crew.
func DeliverableManyForCrew(ctx context.Context, q Querier, crewID string, ids []string) map[string]bool {
	ok := map[string]bool{}
	w, err := WithholdsForCrew(ctx, q, crewID, ids)
	if err != nil {
		return ok
	}
	for _, id := range ids {
		if _, drop := w[id]; !drop {
			ok[id] = true
		}
	}
	return ok
}

// Bound parses a stored allowed_hosts column. bound is false for an empty
// list (the credential is unaffected). For a bound credential readable is
// false when the list cannot be parsed or is empty after parsing: it permits
// no host.
func Bound(rawAllowed string) (hosts []string, bound, readable bool) {
	if strings.TrimSpace(rawAllowed) == "" || strings.TrimSpace(rawAllowed) == "[]" {
		return nil, false, true
	}
	hosts, err := ParseStored(rawAllowed)
	if err != nil || len(hosts) == 0 {
		return nil, true, false
	}
	return hosts, true, true
}

// HostsPermit reports whether target is covered by the approved hosts.
func HostsPermit(hosts []string, target string) bool {
	target = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(target), "."))
	if target == "" || len(hosts) == 0 {
		return false
	}
	return egressallow.NewDomainAllowlist(hosts).IsAllowed(target)
}

// PermitsHost is the single-destination form for a stored allowed_hosts column:
// true when the credential is unbound, or bound with a readable list that
// covers host. An unreadable list permits nothing.
func PermitsHost(rawAllowed, host string) bool {
	hosts, bound, readable := Bound(rawAllowed)
	if !bound {
		return true
	}
	return readable && HostsPermit(hosts, host)
}
