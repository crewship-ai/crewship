package devcontainer

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ValidateMiseConfigInput keeps the original 10 KiB configuration budget while
// allowing a separately bounded native lock. JSON escaping can expand the
// bundle's 512 KiB of decoded file data; the transport envelope is capped too.
func ValidateMiseConfigInput(raw string) error {
	const configLimit = 10 << 10
	const envelopeLimit = 4 << 20
	if raw == "" {
		return nil
	}
	if len(raw) > envelopeLimit {
		return fmt.Errorf("mise_config exceeds 4 MiB envelope limit")
	}
	cfg, err := ParseMiseConfig(raw)
	if err != nil {
		return err
	}
	if cfg.Lock == nil {
		if len(raw) > configLimit {
			return fmt.Errorf("mise_config exceeds 10 KiB configuration limit")
		}
	} else {
		// Count every non-lock field, including unknown passthrough fields. Decoding
		// only into MiseConfig would let ignored fields borrow the lock's allowance.
		if !strings.HasPrefix(strings.TrimSpace(raw), "{") {
			return fmt.Errorf("mise lock requires a JSON envelope")
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &fields); err != nil {
			return err
		}
		delete(fields, "lock")
		rest, err := json.Marshal(fields)
		if err != nil {
			return err
		}
		if len(rest) > configLimit {
			return fmt.Errorf("mise_config exceeds 10 KiB non-lock configuration limit")
		}
	}
	return cfg.Validate()
}
