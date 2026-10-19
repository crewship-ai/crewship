package artifacts

import (
	"errors"
	"testing"

	"github.com/crewship-ai/crewship/internal/documents"
	"github.com/crewship-ai/crewship/internal/pages"
)

func TestRegistryInstalledDocumentAndFlags(t *testing.T) {
	adapter := NewDocumentAdapter(documents.New(nil, documents.Options{}), true)
	flags := EnabledTypes{"doc": true}
	registry := NewRegistry(flags, adapter)
	flags["doc"] = false // Server configuration is captured, not caller-owned mutable policy.
	operations, available := registry.Documents()
	if !available || operations != adapter {
		t.Fatal("installed enabled typed Document adapter unavailable")
	}
	for _, descriptor := range pages.ContentTypes() {
		for _, capability := range registry.Capabilities(descriptor.TypeID, descriptor.SchemaVersion) {
			want := descriptor.TypeID == "doc" && capability.Operation != Export
			if capability.Available != want {
				t.Fatalf("%s/%d: %#v", descriptor.TypeID, descriptor.SchemaVersion, capability)
			}
		}
	}
	if err := registry.Check("doc", 3, Apply); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		registry *Registry
		want     error
	}{
		{"absent adapter", NewRegistry(EnabledTypes{"doc": true}, nil), ErrUnavailable},
		{"absent store", NewRegistry(EnabledTypes{"doc": true}, NewDocumentAdapter(nil, true)), ErrUnavailable},
		{"missing flag", NewRegistry(nil, adapter), ErrDisabled},
		{"false flag", NewRegistry(EnabledTypes{"doc": false}, adapter), ErrDisabled},
		{"adapter disabled", NewRegistry(EnabledTypes{"doc": true}, NewDocumentAdapter(adapter.store, false)), ErrDisabled},
		{"nil registry", nil, ErrUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			if operations, ok := test.registry.Documents(); ok || operations != nil {
				t.Fatal("unavailable registry exposed operations")
			}
			if err := test.registry.Check("doc", 3, Read); !errors.Is(err, test.want) {
				t.Fatalf("got %v want %v", err, test.want)
			}
			for _, capability := range test.registry.Capabilities("doc", 3) {
				if capability.Available {
					t.Fatalf("unavailable adapter advertised %#v", capability)
				}
			}
		})
	}
}

func TestRegistryUnknownAndUnsupportedRemainClosed(t *testing.T) {
	registry := NewRegistry(EnabledTypes{"doc": true, "unknown": true, "slides": true}, NewDocumentAdapter(documents.New(nil, documents.Options{}), true))
	for _, test := range []struct {
		id        string
		schema    int
		operation Operation
	}{
		{"unknown", 1, Read}, {"doc", 99, Read}, {"doc", 3, Operation("execute")}, {"doc", 3, Export},
	} {
		if err := registry.Check(test.id, test.schema, test.operation); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("%#v accepted: %v", test, err)
		}
	}
	first := registry.Capabilities("doc", 3)
	first[0].Available = false
	if !registry.Capabilities("doc", 3)[0].Available {
		t.Fatal("caller mutated registry declaration")
	}
}
