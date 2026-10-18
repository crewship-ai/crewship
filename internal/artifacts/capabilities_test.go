package artifacts

import (
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/pages"
)

func TestCapabilitiesFailClosed(t *testing.T) {
	tests := []struct {
		name, id string
		schema   int
		enabled  EnabledTypes
		op       Operation
		want     error
	}{
		{"zero flags", "doc", 3, nil, Create, ErrDisabled},
		{"explicit false", "doc", 3, EnabledTypes{"doc": false}, Read, ErrDisabled},
		{"unknown type", "unknown", 1, EnabledTypes{"unknown": true}, Read, ErrUnsupported},
		{"unknown schema", "doc", 99, EnabledTypes{"doc": true}, Read, ErrUnsupported},
		{"known format without adapter", "board", 1, EnabledTypes{"board": true}, Apply, ErrUnsupported},
		{"unknown operation", "doc", 3, EnabledTypes{"doc": true}, Operation("execute"), ErrUnsupported},
		{"export not implemented", "doc", 3, EnabledTypes{"doc": true}, Export, ErrUnsupported},
		{"enabled document", "doc", 3, EnabledTypes{"doc": true}, Apply, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := Check(tt.id, tt.schema, tt.enabled, tt.op); !errors.Is(err, tt.want) {
				t.Fatalf("got %v want %v", err, tt.want)
			}
		})
	}
}

func TestEveryCompiledFormatHasExplicitOperationDeclarations(t *testing.T) {
	for _, descriptor := range pages.ContentTypes() {
		capabilities := Capabilities(descriptor.TypeID, descriptor.SchemaVersion, EnabledTypes{descriptor.TypeID: true})
		if len(capabilities) != 7 {
			t.Fatalf("%s/%d: incomplete declarations: %#v", descriptor.TypeID, descriptor.SchemaVersion, capabilities)
		}
		for _, capability := range capabilities {
			want := descriptor.TypeID == "doc" && capability.Operation != Export
			if capability.Supported != want || capability.Available != want {
				t.Fatalf("%#v: %#v", descriptor, capability)
			}
			if !want && capability.Reason == "" {
				t.Fatal("unsupported capability needs a reason")
			}
		}
	}
	// Returned declarations are caller-owned.
	first := Capabilities("doc", 3, EnabledTypes{"doc": true})
	first[0].Available = false
	if !Capabilities("doc", 3, EnabledTypes{"doc": true})[0].Available {
		t.Fatal("caller mutated policy")
	}
}
