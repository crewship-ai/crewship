package devcontainer

import (
	"encoding/json"
	"errors"
	"maps"
	"strings"

	"github.com/crewship-ai/crewship/internal/dockerutil"
)

// ApplyMiseResolution binds a locally reviewed proposal to the current native
// resolver inputs. Hashes detect stale inputs/accidental corruption; they do not
// authenticate the author or prove that mise produced the lock. The regular
// locked installation and optional offline CLI check still run at build time.
func ApplyMiseResolution(cfg *MiseConfig, plan *MiseResolution) (*MiseConfig, error) {
	if cfg == nil || len(cfg.Tools) == 0 || plan == nil || plan.Lock == nil {
		return nil, errors.New("mise apply: configuration and complete resolution are required")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := plan.Lock.Validate(); err != nil {
		return nil, err
	}
	if !dockerutil.IsLocalImageID(plan.ImageID) || (plan.Platform != "linux-x64" && plan.Platform != "linux-arm64") || !miseResolverVersion.MatchString(plan.MiseVersion) {
		return nil, errors.New("mise apply: invalid resolver provenance")
	}
	if plan.SelectorsSHA256 != miseSelectorsDigest(cfg.Tools) || plan.PreviousLockSHA256 != miseLockDigest(cfg.Lock) {
		return nil, errors.New("mise apply: selectors or previous lock changed; resolve a new plan")
	}
	if plan.LockSHA256 != miseLockDigest(plan.Lock) || plan.LockChanged != (plan.LockSHA256 != plan.PreviousLockSHA256) {
		return nil, errors.New("mise apply: result lock does not match the proposal digest")
	}
	result := *cfg
	result.Tools = maps.Clone(cfg.Tools)
	result.Env = maps.Clone(cfg.Env)
	result.Lock = &MiseLockBundle{SchemaVersion: plan.Lock.SchemaVersion, Files: maps.Clone(plan.Lock.Files)}
	return &result, nil
}

// ApplyMiseResolutionInput preserves every non-lock JSON field from the current
// configuration, including fields this binary does not model. Environment values
// never came from the resolver. Native TOML uses the supported tools/env parser.
func ApplyMiseResolutionInput(raw string, plan *MiseResolution) ([]byte, error) {
	if err := ValidateMiseConfigInput(raw); err != nil {
		return nil, err
	}
	cfg, err := ParseMiseConfig(raw)
	if err != nil {
		return nil, err
	}
	updated, err := ApplyMiseResolution(cfg, plan)
	if err != nil {
		return nil, err
	}
	var result []byte
	if strings.HasPrefix(strings.TrimSpace(raw), "{") {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal([]byte(raw), &fields); err != nil {
			return nil, err
		}
		fields["lock"], err = json.Marshal(updated.Lock)
		if err != nil {
			return nil, err
		}
		result, err = json.MarshalIndent(fields, "", "  ")
	} else {
		result, err = json.MarshalIndent(updated, "", "  ")
	}
	if err != nil {
		return nil, err
	}
	if err := ValidateMiseConfigInput(string(result)); err != nil {
		return nil, err
	}
	return result, nil
}
