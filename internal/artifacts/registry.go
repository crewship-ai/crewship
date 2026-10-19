package artifacts

import (
	"errors"
	"fmt"
)

var ErrUnavailable = errors.New("artifact adapter unavailable")

// Registry binds operation declarations to explicitly installed typed adapters.
// Format identity and supported schemas still come from the compiled Page
// registry through Capabilities. No content can install an implementation.
type Registry struct {
	enabled  EnabledTypes
	document DocumentOperations
	slides   *SlidesAdapter
}

// RegistryOption installs a trusted compiled adapter, never content-supplied code.
type RegistryOption func(*Registry)

func WithSlidesAdapter(adapter *SlidesAdapter) RegistryOption {
	return func(r *Registry) { r.slides = adapter }
}

// NewRegistry captures flags and concrete adapters. Existing Document callers
// need no option; an absent optional Slides adapter remains unavailable.
// The concrete constructor input makes nil adapters/stores unambiguous without
// reflection or a new health/authorization protocol on every operation type.
// Returned operations retain each adapter's own enablement checks as well.
func NewRegistry(enabled EnabledTypes, document *DocumentAdapter, options ...RegistryOption) *Registry {
	r := &Registry{enabled: make(EnabledTypes, len(enabled))}
	for id, value := range enabled {
		r.enabled[id] = value
	}
	if document != nil && document.store != nil {
		r.document = document
		r.enabled["doc"] = r.enabled["doc"] && document.enabled
	}
	for _, option := range options {
		if option != nil {
			option(r)
		}
	}
	if r.slides == nil || r.slides.store == nil {
		r.slides = nil
	} else {
		r.enabled["slides"] = r.enabled["slides"] && r.slides.enabled
	}
	return r
}

// Documents returns the installed typed interface only when both registry and
// adapter flags allow it. Callers still supply live transactional authorization.
func (r *Registry) Documents() (DocumentOperations, bool) {
	if r == nil || r.document == nil || !r.enabled["doc"] {
		return nil, false
	}
	return r.document, true
}

// Slides requires both enablement flags and an installed concrete Store adapter.
func (r *Registry) Slides() (SlideOperations, bool) {
	if r == nil || r.slides == nil || !r.enabled["slides"] {
		return nil, false
	}
	return r.slides, true
}

func (r *Registry) Capabilities(typeID string, schema int) []Capability {
	var enabled EnabledTypes
	if r != nil {
		enabled = r.enabled
	}
	result := Capabilities(typeID, schema, enabled)
	// A compiled implementation is supported even when no instance is installed;
	// it must not be advertised as available just because its flag is true.
	if (typeID == "doc" && (r == nil || r.document == nil)) || (typeID == "slides" && (r == nil || r.slides == nil)) {
		for i := range result {
			if result[i].Supported {
				result[i].Available = false
				result[i].Reason = "adapter-unavailable"
			}
		}
	}
	return result
}

func (r *Registry) Check(typeID string, schema int, operation Operation) error {
	for _, capability := range r.Capabilities(typeID, schema) {
		if capability.Operation != operation {
			continue
		}
		if !capability.Supported {
			return fmt.Errorf("%w: %s/%d %s (%s)", ErrUnsupported, typeID, schema, operation, capability.Reason)
		}
		if !capability.Available {
			if capability.Reason == "adapter-unavailable" {
				return fmt.Errorf("%w: %s", ErrUnavailable, typeID)
			}
			return fmt.Errorf("%w: %s", ErrDisabled, typeID)
		}
		return nil
	}
	return fmt.Errorf("%w: %s", ErrUnsupported, operation)
}
