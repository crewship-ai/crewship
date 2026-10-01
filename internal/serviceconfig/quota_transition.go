package serviceconfig

import (
	"encoding/json"
	"fmt"
)

type quotaPolicy struct {
	Name          string `json:"name"`
	QuotaEnforced bool   `json:"quota_enforced"`
	Volumes       []struct {
		Name       string `json:"name"`
		QuotaBytes int64  `json:"quota_bytes"`
		Generation int64  `json:"generation"`
	} `json:"volumes"`
}

// QuotaTransition rejects a services_json edit that changes a quota
// volume's capacity while keeping its generation. A generation's capacity
// is physically fixed (the helper preallocates it once), so the edit could
// never start; a new capacity needs a new generation, which is a new, empty
// volume the operator migrates data into. previous is the stored plaintext
// configuration; an empty, redacted or unparseable previous value imposes
// no constraint (the other validators own those cases).
func QuotaTransition(previous, next string) error {
	var before, after []quotaPolicy
	if previous == "" || previous == Redacted || json.Unmarshal([]byte(previous), &before) != nil {
		return nil
	}
	if err := json.Unmarshal([]byte(next), &after); err != nil {
		return nil
	}
	type volumeKey struct{ service, volume string }
	type fixed struct{ bytes, generation int64 }
	stored := map[volumeKey]fixed{}
	for _, s := range before {
		if !s.QuotaEnforced {
			continue
		}
		for _, v := range s.Volumes {
			stored[volumeKey{s.Name, v.Name}] = fixed{v.QuotaBytes, generationOf(v.Generation)}
		}
	}
	for _, s := range after {
		if !s.QuotaEnforced {
			continue
		}
		for _, v := range s.Volumes {
			old, ok := stored[volumeKey{s.Name, v.Name}]
			if !ok || old.generation != generationOf(v.Generation) || old.bytes == v.QuotaBytes {
				continue
			}
			return fmt.Errorf("services[%q].volumes[%q]: quota_bytes changed from %d to %d without a generation bump; a generation's capacity is fixed, so set generation %d (a new, empty volume) and migrate the data",
				s.Name, v.Name, old.bytes, v.QuotaBytes, old.generation+1)
		}
	}
	return nil
}

func generationOf(g int64) int64 {
	if g == 0 {
		return 1
	}
	return g
}
