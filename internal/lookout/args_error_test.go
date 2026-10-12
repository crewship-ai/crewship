package lookout

import (
	"errors"
	"strings"
	"testing"
)

// TestArgsInvalidError_Error pins the message format that callers may
// surface to operators / agents.
func TestArgsInvalidError_Error(t *testing.T) {
	tests := []struct {
		name string
		err  *ArgsInvalidError
		want string
	}{
		{
			name: "with path",
			err:  &ArgsInvalidError{Path: "args.tools[0].name", Reason: "expected string"},
			want: "lookout: args invalid at args.tools[0].name: expected string",
		},
		{
			name: "no path",
			err:  &ArgsInvalidError{Reason: "value is null"},
			want: "lookout: args invalid: value is null",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestIsArgsInvalid_Roundtrip verifies the type-check predicate.
// Callers branch on this to map validation errors to HTTP 400.
func TestIsArgsInvalid_Roundtrip(t *testing.T) {
	if !IsArgsInvalid(&ArgsInvalidError{Reason: "x"}) {
		t.Error("IsArgsInvalid false negative on direct error")
	}
	wrapped := errors.New("outer: " + (&ArgsInvalidError{Reason: "x"}).Error())
	if IsArgsInvalid(wrapped) {
		t.Error("plain error wrapped via stringification should not match")
	}
	if !IsArgsInvalid(wrapJoin(&ArgsInvalidError{Reason: "x"}, errors.New("other"))) {
		t.Error("IsArgsInvalid false negative on errors.Join")
	}
	if IsArgsInvalid(nil) {
		t.Error("IsArgsInvalid(nil) should be false")
	}
	if IsArgsInvalid(errors.New("plain")) {
		t.Error("IsArgsInvalid plain error → false")
	}
}

// TestValidateArgs_TypeMismatch covers the type-check error path with
// every primitive type.
func TestValidateArgs_TypeMismatch(t *testing.T) {
	tests := []struct {
		name      string
		schema    Schema
		args      map[string]any
		wantPath  string
		wantValid bool
	}{
		{
			name: "string expected, got number",
			schema: Schema{Type: "object", Properties: map[string]Schema{
				"name": {Type: "string"},
			}},
			args:     map[string]any{"name": 42},
			wantPath: "name",
		},
		{
			name: "required missing",
			schema: Schema{Type: "object",
				Properties: map[string]Schema{"name": {Type: "string"}},
				Required:   []string{"name"},
			},
			args:     map[string]any{},
			wantPath: "name",
		},
		{
			name: "enum mismatch",
			schema: Schema{Type: "object", Properties: map[string]Schema{
				"role": {Type: "string", Enum: []any{"OWNER", "ADMIN"}},
			}},
			args:     map[string]any{"role": "VIEWER"},
			wantPath: "role",
		},
		{
			name: "valid args",
			schema: Schema{Type: "object", Properties: map[string]Schema{
				"name": {Type: "string"},
			}},
			args:      map[string]any{"name": "ok"},
			wantValid: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateArgs(tt.schema, tt.args)
			if tt.wantValid {
				if err != nil {
					t.Errorf("expected valid, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want error, got nil")
			}
			var aie *ArgsInvalidError
			if !errors.As(err, &aie) {
				t.Fatalf("want *ArgsInvalidError, got %T: %v", err, err)
			}
			if !strings.Contains(aie.Path, tt.wantPath) {
				t.Errorf("path %q does not contain %q", aie.Path, tt.wantPath)
			}
		})
	}
}

// wrapJoin is errors.Join inlined so this test doesn't depend on
// import-ordering quirks.
func wrapJoin(errs ...error) error {
	return errors.Join(errs...)
}

func TestValidateArgsNestedPrimitiveFailures(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		value      any
		valid      bool
	}{
		{"number", "number", true, false}, {"fractional integer", "integer", 1.5, false},
		{"float32 integer", "integer", float32(2), true}, {"float32 fractional", "integer", float32(2.5), false},
		{"string integer", "integer", "2", false}, {"boolean", "boolean", "true", false},
		{"array", "array", map[string]any{}, false}, {"object", "object", []any{}, false},
		{"null", "null", false, false}, {"null allowed", "null", nil, true},
		{"unknown", "future-type", "text", false}, {"number null", "number", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := Schema{Type: "object", Properties: map[string]Schema{"config": {Type: "object", Properties: map[string]Schema{"value": {Type: tc.kind}}}}}
			err := ValidateArgs(schema, map[string]any{"config": map[string]any{"value": tc.value}})
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var invalid *ArgsInvalidError
			if !errors.As(err, &invalid) || invalid.Path != "config.value" {
				t.Fatalf("wrong correction path: %v", err)
			}
		})
	}
}

func TestValidateArgsUsesStructuralEnumsWithoutPanics(t *testing.T) {
	for _, tc := range []struct {
		name           string
		allowed, value any
		valid          bool
	}{
		{"same-object", map[string]any{"scope": "read"}, map[string]any{"scope": "read"}, true},
		{"different-object", map[string]any{"scope": "read"}, map[string]any{"scope": "write"}, false},
		{"same-array", []any{"read", "list"}, []any{"read", "list"}, true},
		{"different-array", []any{"read"}, []any{"write"}, false},
		{"null-not-listed", "read", nil, false}, {"null-listed", nil, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateArgs(Schema{Type: "object", Properties: map[string]Schema{"permission": {Enum: []any{tc.allowed}}}}, map[string]any{"permission": tc.value})
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
			} else if !IsArgsInvalid(err) {
				t.Fatalf("invalid enum accepted: %v", err)
			}
		})
	}
}
