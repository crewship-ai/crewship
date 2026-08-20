package pipeline

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateSlash(t *testing.T) {
	cases := []struct {
		name    string
		spec    *SlashSpec
		wantErr string // substring; empty = must pass
	}{
		{name: "absent block", spec: nil},
		{name: "enabled, no presentation", spec: &SlashSpec{Enabled: true}},
		{
			name: "full block",
			spec: &SlashSpec{Enabled: true, Label: "Monthly accounting pack", LabelCS: "Účetní podklady za měsíc", Icon: "receipt"},
		},
		{
			// Validated even though it will never reach a palette — the
			// author is looking at the definition NOW, not on the day
			// somebody flips enabled to true.
			name:    "disabled block is still checked",
			spec:    &SlashSpec{Enabled: false, Icon: "Receipt"},
			wantErr: "kebab-case",
		},
		{
			name:    "label too long",
			spec:    &SlashSpec{Enabled: true, Label: strings.Repeat("x", MaxSlashLabelLen+1)},
			wantErr: "slash.label is 81 characters",
		},
		{
			// Runes, not bytes: 80 two-byte characters is a legal label,
			// and counting bytes would have rejected it at 40.
			name: "multibyte label at the limit",
			spec: &SlashSpec{Enabled: true, Label: strings.Repeat("á", MaxSlashLabelLen)},
		},
		{
			name:    "czech label too long",
			spec:    &SlashSpec{Enabled: true, LabelCS: strings.Repeat("á", MaxSlashLabelLen+1)},
			wantErr: "slash.label_cs is 81 characters",
		},
		{
			name:    "label with a newline",
			spec:    &SlashSpec{Enabled: true, Label: "Accounting\npack"},
			wantErr: "single line",
		},
		{
			name:    "padded label",
			spec:    &SlashSpec{Enabled: true, Label: " Accounting pack "},
			wantErr: "whitespace",
		},
		{name: "empty icon is fine", spec: &SlashSpec{Enabled: true, Icon: ""}},
		{name: "kebab-case icon", spec: &SlashSpec{Enabled: true, Icon: "calendar-clock"}},
		{
			name:    "icon with markup",
			spec:    &SlashSpec{Enabled: true, Icon: "<img src=x>"},
			wantErr: "kebab-case",
		},
		{
			name:    "icon too long",
			spec:    &SlashSpec{Enabled: true, Icon: strings.Repeat("a", MaxSlashIconLen+1)},
			wantErr: "over the 40 limit",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateSlash(&DSL{Slash: c.spec})
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("validateSlash() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateSlash() = nil, want error containing %q", c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("validateSlash() = %q, want it to contain %q", err.Error(), c.wantErr)
			}
		})
	}
}

// A definition written before the slash block existed must round-trip
// untouched: no block in, no block out. This is the backwards-compat
// promise the palette rests on — every routine already in a customer
// database stays out of it.
func TestSlashBlockRoundTripsAbsent(t *testing.T) {
	const src = `{"dsl_version":"1.0","name":"legacy-routine","steps":[]}`
	dsl, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if dsl.Slash != nil {
		t.Fatalf("Slash = %+v, want nil for a definition with no slash block", dsl.Slash)
	}
	out, err := json.Marshal(dsl)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if strings.Contains(string(out), "slash") {
		t.Errorf("re-marshalled definition grew a slash key: %s", out)
	}
}

func TestSlashBlockParses(t *testing.T) {
	const src = `{
		"dsl_version":"1.0",
		"name":"msn-etn-podklady",
		"slash":{"enabled":true,"label":"Monthly accounting pack","label_cs":"Účetní podklady za měsíc","icon":"receipt"},
		"steps":[]
	}`
	dsl, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if dsl.Slash == nil {
		t.Fatal("Slash = nil, want the parsed block")
	}
	if !dsl.Slash.Enabled {
		t.Error("Enabled = false, want true")
	}
	if dsl.Slash.Label != "Monthly accounting pack" {
		t.Errorf("Label = %q", dsl.Slash.Label)
	}
	if dsl.Slash.LabelCS != "Účetní podklady za měsíc" {
		t.Errorf("LabelCS = %q", dsl.Slash.LabelCS)
	}
	if dsl.Slash.Icon != "receipt" {
		t.Errorf("Icon = %q", dsl.Slash.Icon)
	}
}
