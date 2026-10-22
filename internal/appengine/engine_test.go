package appengine

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type runnerFunc func(context.Context, []byte, Command) (EngineResult, error)

func (f runnerFunc) Run(c context.Context, b []byte, o Command) (EngineResult, error) {
	return f(c, b, o)
}
func TestGridCraftEffectsRejectUndeclaredCellAndStructuralChanges(t *testing.T) {
	base := json.RawMessage(`{"sheets":[{"name":"Sheet1","cells":{"bands":{},"len":0}}]}`)
	for _, test := range []struct {
		name   string
		result string
		want   error
	}{
		{"one cell", `{"sheets":[{"name":"Sheet1","cells":{"bands":{"0":{"rows":{"1":{"1":{"value":42}}}}},"len":1}}]}`, nil},
		{"another cell", `{"sheets":[{"name":"Sheet1","cells":{"bands":{"0":{"rows":{"8":{"2":{"value":42}}}}},"len":1}}]}`, ErrEffects},
		{"structure", `{"sheets":[{"name":"Renamed","cells":{"bands":{},"len":0}}]}`, ErrEffects},
	} {
		t.Run(test.name, func(t *testing.T) {
			engine := GridCraft{Runner: runnerFunc(func(context.Context, []byte, Command) (EngineResult, error) {
				return EngineResult{Base: base, Result: json.RawMessage(test.result)}, nil
			})}
			b := Binding{Engine: engine.Identity()}
			_, err := engine.Prepare(t.Context(), b, State{Snapshot: base}, Command{Kind: "cell.set", Sheet: "Sheet1", Cell: "B2", Input: "42"})
			if !errors.Is(err, test.want) {
				t.Fatalf("got %v want %v", err, test.want)
			}
		})
	}
}
func TestGridCraftDoesNotRunArbitraryCommands(t *testing.T) {
	engine := GridCraft{Runner: runnerFunc(func(context.Context, []byte, Command) (EngineResult, error) {
		t.Fatal("unapproved command executed")
		return EngineResult{}, nil
	})}
	for _, kind := range []string{"file.open", "file.save", "engine.execute", "ui.click", "range.fill"} {
		_, err := engine.Prepare(t.Context(), Binding{}, State{}, Command{Kind: kind})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s: %v", kind, err)
		}
	}
}
