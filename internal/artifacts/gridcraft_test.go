package artifacts

import (
	"errors"
	"github.com/crewship-ai/crewship/internal/mooring"
	"github.com/crewship-ai/crewship/internal/pages"
	"testing"
)

func TestGridCraftRegistryDefaultOffMissingAdapterAndUnsupportedOperations(t *testing.T) {
	if _, ok := pages.LookupContentType("gridcraft-workbook", 1); !ok {
		t.Fatal("missing compiled workbook descriptor")
	}
	registry := NewRegistry(EnabledTypes{"gridcraft-workbook": true}, nil)
	if err := registry.Check("gridcraft-workbook", 1, Apply); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing adapter: %v", err)
	}
	adapter := NewGridCraftAdapter(mooring.New(nil, mooring.Options{}), nil, false)
	registry = NewRegistry(nil, nil, adapter)
	if _, ok := registry.GridCraft(); ok {
		t.Fatal("default off adapter exposed")
	}
	for _, op := range []Operation{Versions, Restore, Snapshot, Export} {
		if err := Check("gridcraft-workbook", 1, EnabledTypes{"gridcraft-workbook": true}, op); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("unsupported %s: %v", op, err)
		}
	}
}
