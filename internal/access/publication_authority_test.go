package access

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func publicationIdentityFixture() PublicationAuthority {
	binding := PublicationGrantBinding{WorkspaceID: "w", PublisherID: "publisher", PublisherMembershipID: "member", Right: Right{"agent", "agent", "run"}, GrantID: "grant", IssuerID: "issuer", IssuerMembershipID: "issuer-member"}
	return PublicationAuthority{Schema: PublicationAuthoritySchema, WorkspaceID: "w", PipelineID: "root", PublisherID: "publisher", PublisherMembershipID: "member", ActingAgentID: "agent", RootCrewID: "crew", Execution: []PublicationGrantBinding{binding}, Recipes: []PublicationRecipeBinding{{StepID: "nested", PipelineID: "child", VersionID: "version", Version: 1, ContentHash: strings.Repeat("a", 64), AuthorityFingerprint: strings.Repeat("b", 64)}}, Agents: []PublicationAgentBinding{{StepID: "leaf", AgentID: "agent", CrewID: "crew", Profile: "responses_text", PolicyHash: strings.Repeat("c", 64), CredentialID: "credential", GrantID: "provider-grant", CredentialRevision: strings.Repeat("d", 64), Model: "gpt-test", MaxOutputTokens: 4096}}}
}

func TestPublicationCanonicalValidIdentity(t *testing.T) {
	raw, fingerprint, err := CanonicalPublicationAuthority(publicationIdentityFixture())
	if err != nil || len(raw) == 0 || len(fingerprint) != 64 {
		t.Fatalf("valid host-typed manifest must have canonical identity: bytes=%d hash=%q err=%v", len(raw), fingerprint, err)
	}
}

func identityHash(t *testing.T, a PublicationAuthority) string {
	t.Helper()
	_, h, err := CanonicalPublicationAuthority(a)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestPublicationCanonicalOrderAndCopies(t *testing.T) {
	a := publicationIdentityFixture()
	second := a.Execution[0]
	second.Right = Right{"agent", "other", "delegate"}
	second.GrantID = "other-grant"
	a.Execution = append(a.Execution, second)
	receipt := a.Execution[0]
	receipt.Right = Right{"routine", "root", "view"}
	receipt.GrantID = "view-grant"
	a.Authoring = []PublicationGrantBinding{receipt}
	before := append([]PublicationGrantBinding(nil), a.Execution...)
	h := identityHash(t, a)
	if !reflect.DeepEqual(a.Execution, before) {
		t.Fatal("canonicalization mutated caller input")
	}
	slices.Reverse(a.Execution)
	if identityHash(t, a) != h {
		t.Fatal("semantic permutation changed identity")
	}
	// Authoring evidence and runtime bounds are distinct even for the same tuple.
	a.Authoring, a.Execution = a.Execution, a.Authoring
	if identityHash(t, a) == h {
		t.Fatal("authoring receipts collapsed into execution dependencies")
	}
}

func TestPublicationCanonicalEveryIdentityField(t *testing.T) {
	// Reflection makes newly added DTO fields require an explicit validation of
	// their identity effect; json:- must never cause silent field omission.
	for _, section := range []string{"root", "grant", "recipe", "agent"} {
		a := publicationIdentityFixture()
		var target reflect.Value
		switch section {
		case "root":
			target = reflect.ValueOf(&a).Elem()
		case "grant":
			target = reflect.ValueOf(&a.Execution[0]).Elem()
		case "recipe":
			target = reflect.ValueOf(&a.Recipes[0]).Elem()
		case "agent":
			target = reflect.ValueOf(&a.Agents[0]).Elem()
		}
		for i := 0; i < target.NumField(); i++ {
			field := target.Type().Field(i)
			if target.Field(i).Kind() == reflect.Slice {
				continue
			}
			t.Run(section+"/"+field.Name, func(t *testing.T) {
				fresh := publicationIdentityFixture()
				original := identityHash(t, fresh)
				var v reflect.Value
				switch section {
				case "root":
					v = reflect.ValueOf(&fresh).Elem()
				case "grant":
					v = reflect.ValueOf(&fresh.Execution[0]).Elem()
				case "recipe":
					v = reflect.ValueOf(&fresh.Recipes[0]).Elem()
				case "agent":
					v = reflect.ValueOf(&fresh.Agents[0]).Elem()
				}
				f := v.Field(i)
				switch f.Kind() {
				case reflect.String:
					value := f.String()
					if publicationDigest(value) {
						value = "e" + value[1:]
					} else {
						value += "-changed"
					}
					f.SetString(value)
				case reflect.Int, reflect.Int64:
					f.SetInt(f.Int() + 1)
				case reflect.Struct: // Right has three separately exercised fields below.
					f.FieldByName("ID").SetString("other-resource")
				default:
					t.Fatalf("new identity field needs mutation coverage: %s", field.Name)
				}
				_, h, err := CanonicalPublicationAuthority(fresh)
				if err == nil && h == original {
					t.Fatalf("identity field %s omitted from fingerprint", field.Name)
				}
			})
		}
	}
	for _, right := range []Right{{"agent", "agent", "delegate"}, {"routine", "agent", "run"}} {
		a := publicationIdentityFixture()
		before := identityHash(t, a)
		a.Execution[0].Right = right
		if identityHash(t, a) == before {
			t.Fatal("resource kind/operation absent from identity")
		}
	}
}

func TestPublicationCanonicalChildAndAuthorityIdentity(t *testing.T) {
	a := publicationIdentityFixture()
	before := identityHash(t, a)
	// Child content is identical; changing its exact authority still changes root.
	a.Recipes[0].AuthorityFingerprint = strings.Repeat("f", 64)
	if identityHash(t, a) == before {
		t.Fatal("same-content child authorities collapsed")
	}
	a = publicationIdentityFixture()
	a.Recipes[0].VersionID = "another-version"
	if identityHash(t, a) == before {
		t.Fatal("exact child version row omitted")
	}
	// No root Run grant, current role, clock, prompt, revision or runtime adapter
	// appears in this typed input. Structural identity does not admit a run.
	if len(a.Execution) != 1 || a.Execution[0].Right.Kind != "agent" {
		t.Fatal("fixture root Run prerequisite accidentally added")
	}
}

func TestPublicationCanonicalExpiryEquivalence(t *testing.T) {
	a := publicationIdentityFixture()
	a.Agents[0].GrantExpiry = "2027-01-01T01:00:00+01:00"
	b := publicationIdentityFixture()
	b.Agents[0].GrantExpiry = "2027-01-01T00:00:00Z"
	if identityHash(t, a) != identityHash(t, b) {
		t.Fatal("same expiry instant not canonical")
	}
	b.Agents[0].KeyExpiry = "2028-01-01T00:00:00Z"
	if identityHash(t, a) == identityHash(t, b) {
		t.Fatal("key expiry omitted")
	}
}

func TestPublicationCanonicalRejectsMalformedManifest(t *testing.T) {
	tests := map[string]func(*PublicationAuthority){
		"schema":            func(a *PublicationAuthority) { a.Schema = PublicationAuthoritySchema + 1 },
		"missing publisher": func(a *PublicationAuthority) { a.PublisherID = "" },
		"whitespace id":     func(a *PublicationAuthority) { a.PipelineID = " root" },
		"huge id":           func(a *PublicationAuthority) { a.PipelineID = strings.Repeat("x", 257) },
		"foreign grant":     func(a *PublicationAuthority) { a.Execution[0].WorkspaceID = "other" },
		"unknown right":     func(a *PublicationAuthority) { a.Execution[0].Right.Operation = "publish" },
		"missing issuer":    func(a *PublicationAuthority) { a.Execution[0].IssuerMembershipID = "" },
		"duplicate grant":   func(a *PublicationAuthority) { a.Execution = append(a.Execution, a.Execution[0]) },
		"conflicting grant": func(a *PublicationAuthority) {
			b := a.Execution[0]
			b.GrantID = "replacement"
			a.Execution = append(a.Execution, b)
		},
		"bad digest":          func(a *PublicationAuthority) { a.Recipes[0].ContentHash = strings.Repeat("A", 64) },
		"empty authority":     func(a *PublicationAuthority) { a.Recipes[0].AuthorityFingerprint = "" },
		"legacy sentinel":     func(a *PublicationAuthority) { a.Recipes[0].AuthorityFingerprint = "legacy-v1" },
		"nonpositive version": func(a *PublicationAuthority) { a.Recipes[0].Version = 0 },
		"duplicate callsite":  func(a *PublicationAuthority) { a.Recipes = append(a.Recipes, a.Recipes[0]) },
		"mixed callsite":      func(a *PublicationAuthority) { a.Agents[0].StepID = a.Recipes[0].StepID },
		"orphan parent":       func(a *PublicationAuthority) { a.Agents[0].ParentVersionID = "missing-version" },
		"conflicting version": func(a *PublicationAuthority) {
			r := a.Recipes[0]
			r.StepID = "other"
			r.ContentHash = strings.Repeat("e", 64)
			a.Recipes = append(a.Recipes, r)
		},
		"unsupported profile": func(a *PublicationAuthority) { a.Agents[0].Profile = "shell" },
		"invalid expiry":      func(a *PublicationAuthority) { a.Agents[0].GrantExpiry = "tomorrow" },
		"widened output":      func(a *PublicationAuthority) { a.Agents[0].MaxOutputTokens = 4097 },
		"duplicate version number": func(a *PublicationAuthority) {
			r := a.Recipes[0]
			r.StepID = "other"
			r.VersionID = "another-row"
			a.Recipes = append(a.Recipes, r)
		},
		"invalid model":        func(a *PublicationAuthority) { a.Agents[0].Model = "model injected" },
		"control id":           func(a *PublicationAuthority) { a.PipelineID = "root\x00other" },
		"unknown acting agent": func(a *PublicationAuthority) { a.ActingAgentID = "foreign" },
		"empty agents":         func(a *PublicationAuthority) { a.Agents = nil },
		"too many agents":      func(a *PublicationAuthority) { a.Agents = make([]PublicationAgentBinding, 17) },
		"too many recipes":     func(a *PublicationAuthority) { a.Recipes = make([]PublicationRecipeBinding, 65) },
		"too many grants":      func(a *PublicationAuthority) { a.Execution = make([]PublicationGrantBinding, 129) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			a := publicationIdentityFixture()
			mutate(&a)
			raw, h, err := CanonicalPublicationAuthority(a)
			if !errors.Is(err, ErrDenied) || raw != nil || h != "" {
				t.Fatalf("malformed identity accepted: %q %q %v", raw, h, err)
			}
		})
	}
}

func TestPublicationCanonicalHostDTOPrivacy(t *testing.T) {
	a := publicationIdentityFixture()
	for _, dto := range []any{a, a.Execution[0], a.Recipes[0], a.Agents[0]} {
		raw, err := json.Marshal(dto)
		if err != nil || string(raw) != "{}" {
			t.Fatalf("host identity exposed in generic JSON: %s %v", raw, err)
		}
	}
}

func TestPublicationCanonicalValidAlternateBindings(t *testing.T) {
	changes := map[string]func(*PublicationAuthority){
		"declaration profile":   func(a *PublicationAuthority) { a.Agents[0].Profile = "native_api_key" },
		"child parent callsite": func(a *PublicationAuthority) { a.Agents[0].ParentVersionID = "version" },
		"grant expiry":          func(a *PublicationAuthority) { a.Agents[0].GrantExpiry = "2027-01-01T00:00:00Z" },
		"publisher incarnation": func(a *PublicationAuthority) {
			a.PublisherMembershipID = "new-member"
			a.Execution[0].PublisherMembershipID = "new-member"
		},
		"publisher principal": func(a *PublicationAuthority) { a.PublisherID = "new-user"; a.Execution[0].PublisherID = "new-user" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			a := publicationIdentityFixture()
			before := identityHash(t, a)
			change(&a)
			if identityHash(t, a) == before {
				t.Fatal("valid alternate binding did not change identity")
			}
		})
	}
}

func TestPublicationCanonicalGraphPermutation(t *testing.T) {
	a := publicationIdentityFixture()
	r := a.Recipes[0]
	r.StepID = "second-child"
	a.Recipes = append(a.Recipes, r)
	agent := a.Agents[0]
	agent.StepID = "second-leaf"
	a.Agents = append(a.Agents, agent)
	beforeRecipes := append([]PublicationRecipeBinding(nil), a.Recipes...)
	beforeAgents := append([]PublicationAgentBinding(nil), a.Agents...)
	before := identityHash(t, a)
	if !reflect.DeepEqual(a.Recipes, beforeRecipes) || !reflect.DeepEqual(a.Agents, beforeAgents) {
		t.Fatal("canonicalization changed input graph")
	}
	slices.Reverse(a.Recipes)
	slices.Reverse(a.Agents)
	if identityHash(t, a) != before {
		t.Fatal("graph permutation changed canonical identity")
	}
}

func TestPublicationCanonicalRejectsInvalidUTF8Identity(t *testing.T) {
	a := publicationIdentityFixture()
	a.PipelineID = "root\x80"
	b := publicationIdentityFixture()
	b.PipelineID = "root\x81"
	_, first, firstErr := CanonicalPublicationAuthority(a)
	_, second, secondErr := CanonicalPublicationAuthority(b)
	if firstErr == nil || secondErr == nil {
		t.Errorf("invalid UTF8 identities accepted: first=%v second=%v", firstErr, secondErr)
	}
	if firstErr == nil && secondErr == nil && first == second {
		t.Log("distinct invalid UTF8 IDs collapsed to the same JSON fingerprint")
	}
}

func TestPublicationCanonicalLegacyChildAndUnicode(t *testing.T) {
	a := publicationIdentityFixture()
	before := identityHash(t, a)
	a.Recipes[0].AuthorityFingerprint = LegacyPublicationAuthorityFingerprint
	if identityHash(t, a) == before {
		t.Fatal("explicit legacy child identity collapsed into bounded child")
	}
	// Valid Unicode is exact identity; do not impose a new ASCII-only policy.
	a.PipelineID = "rutina-žluťoučký"
	identityHash(t, a)
}

func TestPublicationCanonicalRejectsOversizedBytes(t *testing.T) {
	a := publicationIdentityFixture()
	a.WorkspaceID = strings.Repeat("w", 256)
	a.PublisherID = strings.Repeat("p", 256)
	a.PublisherMembershipID = strings.Repeat("m", 256)
	template := a.Execution[0]
	template.WorkspaceID = a.WorkspaceID
	template.PublisherID = a.PublisherID
	template.PublisherMembershipID = a.PublisherMembershipID
	template.GrantID = strings.Repeat("g", 256)
	template.IssuerID = strings.Repeat("i", 256)
	template.IssuerMembershipID = strings.Repeat("j", 256)
	a.Authoring = nil
	a.Execution = nil
	for i := 0; i < 128; i++ {
		b := template
		b.Right.ID = fmt.Sprintf("%0256d", i)
		b.GrantID = fmt.Sprintf("%0256d", i)
		a.Authoring = append(a.Authoring, b)
		a.Execution = append(a.Execution, b)
	}
	raw, h, err := CanonicalPublicationAuthority(a)
	if !errors.Is(err, ErrDenied) || raw != nil || h != "" {
		t.Fatal("oversized canonical bytes accepted")
	}
}

func TestPublicationCanonicalRejectsContradictoryGrantIdentity(t *testing.T) {
	for _, acrossClasses := range []bool{false, true} {
		a := publicationIdentityFixture()
		contradictory := a.Execution[0]
		contradictory.Right.ID = "different-agent"
		if acrossClasses {
			a.Authoring = []PublicationGrantBinding{contradictory}
		} else {
			a.Execution = append(a.Execution, contradictory)
		}
		raw, h, err := CanonicalPublicationAuthority(a)
		if !errors.Is(err, ErrDenied) || raw != nil || h != "" {
			t.Fatalf("same immutable grant ID accepted for different rights (across=%v)", acrossClasses)
		}
	}
}

func TestPublicationCanonicalSameReceiptAndDependencyRemainSeparate(t *testing.T) {
	a := publicationIdentityFixture()
	before := identityHash(t, a)
	a.Authoring = append(a.Authoring, a.Execution[0])
	if identityHash(t, a) == before {
		t.Fatal("separate authoring evidence was omitted")
	}
}

func TestPublicationCanonicalDeclaredAuthorAgentIdentity(t *testing.T) {
	a := publicationIdentityFixture()
	a.AuthorAgentID = "declared-controller"
	first := identityHash(t, a)
	a.AuthorAgentID = "different-controller"
	if identityHash(t, a) == first {
		t.Fatal("different declared author agents collapsed into one authority despite identical recipe/dependencies")
	}
}

func TestPublicationCanonicalOptionalDeclaredAuthor(t *testing.T) {
	for _, author := range []string{"", "separate-author", "autor-č"} {
		a := publicationIdentityFixture()
		a.AuthorAgentID = author
		raw, _, err := CanonicalPublicationAuthority(a)
		if err != nil {
			t.Fatalf("optional declared author %q: %v", author, err)
		}
		var header map[string]json.RawMessage
		if err := json.Unmarshal(raw, &header); err != nil {
			t.Fatal(err)
		}
		var got string
		if value, ok := header["author_agent"]; !ok {
			t.Fatal("canonical header omitted author_agent")
		} else if err := json.Unmarshal(value, &got); err != nil || got != author {
			t.Fatalf("author projection mismatch %q %v", got, err)
		}
		public, err := json.Marshal(a)
		if err != nil || string(public) != "{}" {
			t.Fatalf("private author leaked: %s %v", public, err)
		}
	}
	for _, author := range []string{" bad", "bad\n", string([]byte{0x80}), strings.Repeat("a", 257)} {
		a := publicationIdentityFixture()
		a.AuthorAgentID = author
		if _, _, err := CanonicalPublicationAuthority(a); !errors.Is(err, ErrDenied) {
			t.Fatalf("malformed author accepted: %v", err)
		}
	}
}
