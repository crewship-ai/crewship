// Package artifacts describes available typed operations over existing Page
// content. Format identity remains owned by the compiled pages registry.
package artifacts

import (
	"errors"
	"fmt"

	"github.com/crewship-ai/crewship/internal/pages"
)

type Operation string

const (
	Create   Operation = "create"
	Read     Operation = "read"
	Apply    Operation = "apply"
	Versions Operation = "versions"
	Restore  Operation = "restore"
	Snapshot Operation = "snapshot"
	Export   Operation = "export"
)

var (
	ErrUnsupported = errors.New("unsupported artifact operation")
	ErrDisabled    = errors.New("artifact type disabled")
)

// EnabledTypes is server configuration, never a document-supplied grant. Missing
// entries are disabled. Availability does not grant permission to any actor.
type EnabledTypes map[string]bool

type Capability struct {
	Operation Operation `json:"operation"`
	Supported bool      `json:"supported"`
	Available bool      `json:"available"`
	Reason    string    `json:"reason,omitempty"`
}

// Capabilities returns declarations for every operation, including unsupported
// ones. A known descriptor or export format alone never enables an operation.
func Capabilities(typeID string, schema int, enabled EnabledTypes) []Capability {
	_, known := pages.LookupContentType(typeID, schema)
	result := make([]Capability, 0, 7)
	for _, operation := range []Operation{Create, Read, Apply, Versions, Restore, Snapshot, Export} {
		c := Capability{Operation: operation}
		switch {
		case !known || (typeID == "slides" && schema != 1):
			c.Reason = "unsupported-type-or-schema"
		case (typeID != "doc" && typeID != "slides") || operation == Export || (typeID == "slides" && operation == Snapshot):
			c.Reason = "adapter-unavailable"
		default:
			c.Supported = true
			c.Available = enabled[typeID]
			if !c.Available {
				c.Reason = "type-disabled"
			}
		}
		result = append(result, c)
	}
	return result
}

func Check(typeID string, schema int, enabled EnabledTypes, operation Operation) error {
	for _, capability := range Capabilities(typeID, schema, enabled) {
		if capability.Operation != operation {
			continue
		}
		if !capability.Supported {
			return fmt.Errorf("%w: %s/%d %s (%s)", ErrUnsupported, typeID, schema, operation, capability.Reason)
		}
		if !capability.Available {
			return fmt.Errorf("%w: %s", ErrDisabled, typeID)
		}
		return nil
	}
	return fmt.Errorf("%w: %s", ErrUnsupported, operation)
}
