package mooring

import (
	"encoding/json"
	protocol "github.com/crewship-ai/crewship/mooring/protocol/generated/go"
	"sort"
	"strconv"
)

// Structural validation only: Go does not parse Yrs or turn writer identifiers
// into authority. Exact application replay and allocation extraction belong to
// the pinned concrete Rust adapter; source proof belongs to sealed host ingress.
func freezeSessionNativeDelta(delta *protocol.SessionNativeDelta, effects []BatchOperationEffects) (*protocol.SessionNativeDelta, error) {
	if delta == nil || delta.AllocationOrigins == nil || delta.Targets == nil || len(delta.AllocationOrigins) > 256 || len(delta.Targets) == 0 || len(delta.Targets) > 256 {
		return nil, ErrPrivateControl
	}
	origin := func(text string) (uint64, error) {
		n, err := strconv.ParseUint(text, 10, 64)
		if err != nil || n == 0 || n > (1<<53)-1 || strconv.FormatUint(n, 10) != text {
			return 0, ErrPrivateControl
		}
		return n, nil
	}
	writer, err := origin(string(delta.WriterOrigin))
	if err != nil {
		return nil, err
	}
	var prior uint64
	for _, entry := range delta.AllocationOrigins {
		n, e := origin(string(entry))
		if e != nil || n != writer || n <= prior {
			return nil, ErrPrivateControl
		}
		prior = n
	}
	raw, err := checkpointBase64(delta.Update, 262144)
	if err != nil || len(raw) == 0 {
		return nil, ErrPrivateControl
	}
	targets := make(map[string]struct{})
	for _, effect := range effects {
		for _, target := range effect.Targets {
			targets[target.TargetID] = struct{}{}
		}
	}
	ordered := make([]string, 0, len(targets))
	for target := range targets {
		ordered = append(ordered, target)
	}
	sort.Strings(ordered)
	if len(ordered) != len(delta.Targets) {
		return nil, ErrPrivateControl
	}
	for i, target := range delta.Targets {
		if !batchID(string(target)) || string(target) != ordered[i] {
			return nil, ErrPrivateControl
		}
	}
	encoded, err := json.Marshal(delta)
	if err != nil || len(encoded) > MaxPrivatePrepareRPCFrame {
		return nil, ErrPrivateControl
	}
	var owned protocol.SessionNativeDelta
	if json.Unmarshal(encoded, &owned) != nil {
		return nil, ErrPrivateControl
	}
	return &owned, nil
}
