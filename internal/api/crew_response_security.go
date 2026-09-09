package api

import (
	"encoding/json"

	"github.com/crewship-ai/crewship/internal/serviceconfig"
)

// publicServiceConfig protects this field across all crew response envelopes.
// Do not implement MarshalJSON on crewResponse itself: embedding it in a
// response with warnings promotes that method and silently drops the warnings.
type publicServiceConfig string

func (c publicServiceConfig) MarshalJSON() ([]byte, error) {
	return json.Marshal(serviceconfig.Public(string(c)))
}
