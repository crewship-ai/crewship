package pipeline

import (
	"context"
	"encoding/json"
	"testing"
)

func TestCelCodeRunner(t *testing.T) {
	r := CelCodeRunner{}
	ctx := context.Background()

	cases := []struct {
		name    string
		code    string
		inputs  map[string]any
		want    string
		wantErr bool
	}{
		{"simple gt true", "inputs.spend > inputs.threshold", map[string]any{"spend": 9.0, "threshold": 5.0}, "true", false},
		{"simple gt false", "inputs.spend > inputs.threshold", map[string]any{"spend": 2.0, "threshold": 5.0}, "false", false},
		{"compound and", `inputs.spend > 5.0 && inputs.region == "eu"`, map[string]any{"spend": 9.0, "region": "eu"}, "true", false},
		{"compound and false", `inputs.spend > 5.0 && inputs.region == "eu"`, map[string]any{"spend": 9.0, "region": "us"}, "false", false},
		{"string result", `inputs.spend > 5.0 ? "spike" : "ok"`, map[string]any{"spend": 9.0}, "spike", false},
		{"arithmetic", "inputs.a + inputs.b", map[string]any{"a": 2.0, "b": 3.0}, "5", false},
		{"json.Number coercion", "inputs.spend > inputs.threshold", map[string]any{"spend": json.Number("9"), "threshold": json.Number("5")}, "true", false},
		{"list membership", `inputs.tier in ["gold", "platinum"]`, map[string]any{"tier": "gold"}, "true", false},
		{"compile error", "inputs.spend >>> 5", map[string]any{"spend": 1.0}, "", true},
		{"unknown var", "missing.field > 1", map[string]any{}, "", true},
		{"empty expr", "", map[string]any{}, "", true},
		{"cost-limited expr still evaluates under budget", "[1,2,3].map(x, x * 2).size() == 3", map[string]any{}, "true", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := r.RunCode(ctx, CodeRunRequest{Runtime: "cel", Code: tc.code, Inputs: tc.inputs})
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got stdout %q", res.Stdout)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v (stderr %q)", err, res.Stderr)
			}
			if res.Stdout != tc.want {
				t.Fatalf("got %q, want %q", res.Stdout, tc.want)
			}
			if res.ExitCode != 0 {
				t.Fatalf("exit code %d, want 0", res.ExitCode)
			}
		})
	}
}

// The dispatching runner routes by runtime and rejects unwired ones.
func TestMultiCodeRunner_Dispatch(t *testing.T) {
	r := NewMultiCodeRunner()
	ctx := context.Background()

	// expr still works
	res, err := r.RunCode(ctx, CodeRunRequest{Runtime: "expr", Code: "3 > 5", InputEnv: map[string]string{}})
	if err != nil || res.Stdout != "false" {
		t.Fatalf("expr dispatch: stdout=%q err=%v", res.Stdout, err)
	}
	// cel routes to CEL
	res, err = r.RunCode(ctx, CodeRunRequest{Runtime: "cel", Code: "1 + 1", Inputs: map[string]any{}})
	if err != nil || res.Stdout != "2" {
		t.Fatalf("cel dispatch: stdout=%q err=%v", res.Stdout, err)
	}
	// bash is unwired → error
	if _, err := r.RunCode(ctx, CodeRunRequest{Runtime: "bash", Code: "echo hi"}); err == nil {
		t.Fatal("expected error for unwired runtime bash")
	}
}
