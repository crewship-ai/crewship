package llm

import (
	"reflect"
	"testing"
)

// The registry replaced three copies of the same three-way choice, so the
// property under test is not "the table has rows" but "the table still says
// exactly what the copies said". Order included: this list reaches the admin
// console's provider picker via keepercfg.AuxProviders, and a sorted order
// would reorder an operator's dropdown for no reason.
func TestRegisteredProviders_DeclarationOrder(t *testing.T) {
	want := []string{"anthropic", "openai", "ollama"}
	if got := RegisteredProviders(); !reflect.DeepEqual(got, want) {
		t.Errorf("RegisteredProviders() = %v, want %v", got, want)
	}
}

// The returned slice is a copy — a caller that sorts it in place (the obvious
// thing to do before rendering) must not sort the registry.
func TestRegisteredProviders_IsACopy(t *testing.T) {
	first := RegisteredProviders()
	first[0] = "clobbered"
	if got := RegisteredProviders()[0]; got != "anthropic" {
		t.Errorf("mutating the result changed the registry: [0] = %q, want anthropic", got)
	}
}

// LookupProvider lowercases and trims, because internal/api carries the
// provider as an uppercase enum value and keepercfg stores it lowercase. The
// old exact switch made the same provider supported on one side of a call and
// unsupported on the other.
func TestLookupProvider(t *testing.T) {
	tests := []struct {
		name   string
		id     string
		wantID string
		wantOK bool
	}{
		{"exact lowercase", "anthropic", "anthropic", true},
		{"uppercase enum form", "ANTHROPIC", "anthropic", true},
		{"mixed case", "OpenAI", "openai", true},
		{"surrounding whitespace", "  ollama  ", "ollama", true},
		{"whitespace and case together", " Anthropic\t", "anthropic", true},
		{"empty", "", "", false},
		{"whitespace only", "   ", "", false},
		{"google is deliberately absent", "google", "", false},
		{"gemini is deliberately absent", "gemini", "", false},
		{"unknown vendor", "cohere", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec, ok := LookupProvider(tt.id)
			if ok != tt.wantOK {
				t.Fatalf("LookupProvider(%q) ok = %v, want %v", tt.id, ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if spec.ID != tt.wantID {
				t.Errorf("LookupProvider(%q).ID = %q, want %q", tt.id, spec.ID, tt.wantID)
			}
		})
	}
}

// Every row must be constructible. A spec missing New or a required label is a
// row that resolves and then fails at first use, which is the failure mode the
// registry exists to remove.
func TestRegisteredProviderSpecs_AreComplete(t *testing.T) {
	specs := RegisteredProviderSpecs()
	if len(specs) != len(RegisteredProviders()) {
		t.Fatalf("specs = %d rows, ids = %d", len(specs), len(RegisteredProviders()))
	}
	for _, s := range specs {
		t.Run(s.ID, func(t *testing.T) {
			if s.ID == "" {
				t.Error("empty ID")
			}
			if s.DisplayName == "" {
				t.Error("empty DisplayName")
			}
			if s.Codec == "" {
				t.Error("empty Codec")
			}
			if s.Auth == "" {
				t.Error("empty Auth")
			}
			if s.New == nil {
				t.Error("nil New")
			}
		})
	}
}

// The specs the aux builder and the pricing key depend on, spelled out. These
// are the values that used to be literals in three files.
func TestRegisteredProviderSpecs_Values(t *testing.T) {
	tests := []struct {
		id              string
		displayName     string
		codec           Codec
		auth            AuthScheme
		keyEnv          string
		baseEnv         string
		baseDefault     string
		catalogID       string
		defaultAuxModel string
		maxTokensField  string
	}{
		{
			id: "anthropic", displayName: "Anthropic",
			codec: CodecAnthropicMessages, auth: AuthAnthropicKey,
			keyEnv: "ANTHROPIC_API_KEY", baseEnv: "",
			baseDefault: "https://api.anthropic.com/v1/messages",
			catalogID:   "anthropic", defaultAuxModel: "claude-haiku-4-5",
		},
		{
			id: "openai", displayName: "OpenAI",
			codec: CodecOpenAICompat, auth: AuthBearer,
			keyEnv: "OPENAI_API_KEY", baseEnv: "",
			baseDefault: "https://api.openai.com/v1/chat/completions",
			catalogID:   "openai", defaultAuxModel: "gpt-5.4-mini",
			maxTokensField: "max_completion_tokens",
		},
		{
			// No KeyEnv: the local judge needs no credential, and declaring
			// one would turn a working local slot into a hard error.
			id: "ollama", displayName: "Ollama",
			codec: CodecOllamaNative, auth: AuthNone,
			keyEnv: "", baseEnv: "KEEPER_OLLAMA_URL",
			baseDefault: "http://localhost:11434",
			catalogID:   "", defaultAuxModel: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			s, ok := LookupProvider(tt.id)
			if !ok {
				t.Fatalf("%q not registered", tt.id)
			}
			if s.DisplayName != tt.displayName {
				t.Errorf("DisplayName = %q, want %q", s.DisplayName, tt.displayName)
			}
			if s.Codec != tt.codec {
				t.Errorf("Codec = %q, want %q", s.Codec, tt.codec)
			}
			if s.Auth != tt.auth {
				t.Errorf("Auth = %q, want %q", s.Auth, tt.auth)
			}
			if s.KeyEnv != tt.keyEnv {
				t.Errorf("KeyEnv = %q, want %q", s.KeyEnv, tt.keyEnv)
			}
			if s.BaseEnv != tt.baseEnv {
				t.Errorf("BaseEnv = %q, want %q", s.BaseEnv, tt.baseEnv)
			}
			if s.BaseDefault != tt.baseDefault {
				t.Errorf("BaseDefault = %q, want %q", s.BaseDefault, tt.baseDefault)
			}
			if s.CatalogID != tt.catalogID {
				t.Errorf("CatalogID = %q, want %q", s.CatalogID, tt.catalogID)
			}
			if s.DefaultAuxModel != tt.defaultAuxModel {
				t.Errorf("DefaultAuxModel = %q, want %q", s.DefaultAuxModel, tt.defaultAuxModel)
			}
			if s.MaxTokensField != tt.maxTokensField {
				t.Errorf("MaxTokensField = %q, want %q", s.MaxTokensField, tt.maxTokensField)
			}
		})
	}
}

// RegisteredProviderSpecs hands out copies, so a caller that edits a spec it
// received (blanking a key env before logging it, say) cannot reach into the
// table every later build reads.
func TestRegisteredProviderSpecs_AreCopies(t *testing.T) {
	specs := RegisteredProviderSpecs()
	for i := range specs {
		specs[i].KeyEnv = "CLOBBERED"
		specs[i].DisplayName = "clobbered"
	}
	s, ok := LookupProvider("anthropic")
	if !ok {
		t.Fatal("anthropic vanished from the registry")
	}
	if s.KeyEnv != "ANTHROPIC_API_KEY" || s.DisplayName != "Anthropic" {
		t.Errorf("mutating a returned spec changed the registry: %+v", s)
	}
}

// RegisterProvider panics rather than returning an error: every case below is
// a mistake in a package init, where there is no caller to hand an error to
// and a silently-skipped provider would surface much later as "unsupported aux
// provider" on an id the code plainly declares.
//
// None of these rows may reach the map — the panic fires before the write —
// which is also what keeps this test from polluting the registry the rest of
// the package reads.
func TestRegisterProvider_PanicsOnInvalidSpec(t *testing.T) {
	ok := func(m AuxModel, base, apiKey string) (Provider, error) { return nil, nil }
	valid := ProviderSpec{
		ID: "test-only-provider", DisplayName: "Test",
		Codec: CodecOpenAICompat, Auth: AuthBearer, New: ok,
	}
	without := func(mut func(*ProviderSpec)) ProviderSpec {
		s := valid
		mut(&s)
		return s
	}

	tests := []struct {
		name string
		spec ProviderSpec
	}{
		{"empty ID", without(func(s *ProviderSpec) { s.ID = "" })},
		{"empty DisplayName", without(func(s *ProviderSpec) { s.DisplayName = "" })},
		{"empty Codec", without(func(s *ProviderSpec) { s.Codec = "" })},
		{"empty Auth", without(func(s *ProviderSpec) { s.Auth = "" })},
		{"nil New", without(func(s *ProviderSpec) { s.New = nil })},
		{"duplicate id", without(func(s *ProviderSpec) { s.ID = "anthropic" })},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("RegisterProvider(%+v) did not panic", tt.spec)
				}
			}()
			RegisterProvider(tt.spec)
		})
	}

	// The failed registrations left nothing behind.
	if got := RegisteredProviders(); !reflect.DeepEqual(got, []string{"anthropic", "openai", "ollama"}) {
		t.Errorf("registry mutated by failed registrations: %v", got)
	}
	if _, found := LookupProvider("test-only-provider"); found {
		t.Error("an invalid spec reached the registry")
	}
}
