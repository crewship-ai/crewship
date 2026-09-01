package sidecar

import (
	"sync"
	"testing"
)

func TestCredStoreLoad(t *testing.T) {
	cs := NewCredStore()
	cs.Load([]Credential{
		{ID: "c1", Provider: ProviderAnthropic, Token: "sk-ant-1", Priority: 1},
		{ID: "c2", Provider: ProviderAnthropic, Token: "sk-ant-2", Priority: 2},
		{ID: "c3", Provider: ProviderOpenAI, Token: "sk-oai-1", Priority: 1},
	})

	if cs.Count(ProviderAnthropic) != 2 {
		t.Errorf("expected 2 anthropic creds, got %d", cs.Count(ProviderAnthropic))
	}
	if cs.Count(ProviderOpenAI) != 1 {
		t.Errorf("expected 1 openai cred, got %d", cs.Count(ProviderOpenAI))
	}
	if cs.Count(ProviderGoogle) != 0 {
		t.Errorf("expected 0 google creds, got %d", cs.Count(ProviderGoogle))
	}
}

func TestCredStoreSelectRoundRobin(t *testing.T) {
	cs := NewCredStore()
	cs.Load([]Credential{
		{ID: "c1", Provider: ProviderAnthropic, Token: "sk-ant-1"},
		{ID: "c2", Provider: ProviderAnthropic, Token: "sk-ant-2"},
	})

	first := cs.Select(ProviderAnthropic, "")
	if first == nil || first.ID != "c1" {
		t.Fatalf("expected c1, got %v", first)
	}

	second := cs.Select(ProviderAnthropic, "")
	if second == nil || second.ID != "c2" {
		t.Fatalf("expected c2, got %v", second)
	}

	// Should wrap around
	third := cs.Select(ProviderAnthropic, "")
	if third == nil || third.ID != "c1" {
		t.Fatalf("expected c1 (wrap), got %v", third)
	}
}

func TestCredStoreSelectEmpty(t *testing.T) {
	cs := NewCredStore()
	if cs.Select(ProviderAnthropic, "") != nil {
		t.Error("expected nil for empty store")
	}
}

func TestCredStoreSelectWrongProvider(t *testing.T) {
	cs := NewCredStore()
	cs.Load([]Credential{
		{ID: "c1", Provider: ProviderAnthropic, Token: "sk-ant-1"},
	})

	if cs.Select(ProviderOpenAI, "") != nil {
		t.Error("expected nil for wrong provider")
	}
}

func TestCredStoreRemove(t *testing.T) {
	cs := NewCredStore()
	cs.Load([]Credential{
		{ID: "c1", Provider: ProviderAnthropic, Token: "sk-ant-1"},
		{ID: "c2", Provider: ProviderAnthropic, Token: "sk-ant-2"},
	})

	cs.Remove("c1")

	if cs.Count(ProviderAnthropic) != 1 {
		t.Errorf("expected 1 after removal, got %d", cs.Count(ProviderAnthropic))
	}

	cred := cs.Select(ProviderAnthropic, "")
	if cred == nil || cred.ID != "c2" {
		t.Fatalf("expected c2 after removing c1, got %v", cred)
	}
}

// Remove must reset the round-robin counters exactly like Reap does (#1139
// review nit — the two removal paths had drifted inconsistent). Three
// same-priority creds, one Select to advance the counter by one tick, then
// Remove an unrelated credential: without a reset the next Select lands on
// the ticket the stale counter dictates (c2); with the reset it restarts
// clean at ticket 0 (c1).
func TestCredStoreRemove_ResetsRoundRobin(t *testing.T) {
	cs := NewCredStore()
	cs.Load([]Credential{
		{ID: "c1", Provider: ProviderAnthropic, Token: "t1"},
		{ID: "c2", Provider: ProviderAnthropic, Token: "t2"},
		{ID: "c3", Provider: ProviderAnthropic, Token: "t3"},
	})

	if first := cs.Select(ProviderAnthropic, ""); first == nil || first.ID != "c1" {
		t.Fatalf("expected c1, got %v", first)
	}

	cs.Remove("c3") // shrinks the top tier from 3 to 2, unrelated to the ticket above

	if next := cs.Select(ProviderAnthropic, ""); next == nil || next.ID != "c1" {
		t.Fatalf("expected c1 (round-robin reset by Remove, matching Reap), got %v", next)
	}
}

func TestCredStoreLoadReplacesAll(t *testing.T) {
	cs := NewCredStore()
	cs.Load([]Credential{
		{ID: "c1", Provider: ProviderAnthropic, Token: "old"},
	})
	cs.Load([]Credential{
		{ID: "c2", Provider: ProviderOpenAI, Token: "new"},
	})

	if cs.Count(ProviderAnthropic) != 0 {
		t.Error("old credentials should be replaced")
	}
	if cs.Count(ProviderOpenAI) != 1 {
		t.Error("new credentials should be loaded")
	}
}

func TestCredStoreSelectPriorityAware(t *testing.T) {
	cs := NewCredStore()
	cs.Load([]Credential{
		{ID: "low", Provider: ProviderAnthropic, Token: "sk-low", Priority: 2},
		{ID: "high1", Provider: ProviderAnthropic, Token: "sk-high1", Priority: 1},
		{ID: "high2", Provider: ProviderAnthropic, Token: "sk-high2", Priority: 1},
	})

	// Should only round-robin within the highest-priority (Priority=1) tier
	first := cs.Select(ProviderAnthropic, "")
	if first == nil || first.Priority != 1 {
		t.Fatalf("expected priority 1 cred, got %v", first)
	}
	second := cs.Select(ProviderAnthropic, "")
	if second == nil || second.Priority != 1 {
		t.Fatalf("expected priority 1 cred, got %v", second)
	}
	// Both selects should be from {high1, high2}
	if first.ID == second.ID {
		t.Errorf("expected round-robin between high1/high2, got same: %s", first.ID)
	}
	// Third call wraps around
	third := cs.Select(ProviderAnthropic, "")
	if third == nil || third.ID != first.ID {
		t.Errorf("expected wrap-around to %s, got %v", first.ID, third)
	}
}

func TestCredStoreSelectSinglePriority(t *testing.T) {
	cs := NewCredStore()
	cs.Load([]Credential{
		{ID: "c1", Provider: ProviderAnthropic, Token: "sk-1", Priority: 5},
		{ID: "c2", Provider: ProviderAnthropic, Token: "sk-2", Priority: 5},
	})
	// Same priority: normal round-robin
	first := cs.Select(ProviderAnthropic, "")
	second := cs.Select(ProviderAnthropic, "")
	if first.ID == second.ID {
		t.Error("expected round-robin between c1/c2")
	}
}

func TestCredStoreConcurrentAccess(t *testing.T) {
	cs := NewCredStore()
	cs.Load([]Credential{
		{ID: "c1", Provider: ProviderAnthropic, Token: "sk-1"},
		{ID: "c2", Provider: ProviderAnthropic, Token: "sk-2"},
	})

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cs.Select(ProviderAnthropic, "")
		}()
	}
	wg.Wait()
}

// TestCredStoreCountsByProvider is the replacement for /health's N× Count
// calls: one pass, one lock acquisition, one count per provider actually held.
// Table-driven over the shapes /health can encounter, including the two the
// old per-provider loop could not express — a provider with no descriptor, and
// an empty store.
func TestCredStoreCountsByProvider(t *testing.T) {
	tests := []struct {
		name  string
		creds []Credential
		want  map[string]int
	}{
		{
			name: "empty store reports nothing",
			want: map[string]int{},
		},
		{
			name: "multiple providers, multiple credentials each",
			creds: []Credential{
				{ID: "a1", Provider: ProviderAnthropic, Token: "sk-ant-1"},
				{ID: "a2", Provider: ProviderAnthropic, Token: "sk-ant-2"},
				{ID: "o1", Provider: ProviderOpenAI, Token: "sk-oai-1"},
				{ID: "r1", Provider: ProviderOpenRouter, Token: "sk-or-1"},
				{ID: "c1", Provider: ProviderOpenAICompat, Token: "sk-c-1", BaseURL: "https://llm.example/v1"},
			},
			want: map[string]int{"ANTHROPIC": 2, "OPENAI": 1, "OPENROUTER": 1, "OPENAI_COMPAT": 1},
		},
		{
			// CURSOR/FACTORY are env-injected and have no route descriptor, but
			// the store still holds them. CountsByProvider reports what the
			// STORE holds; deciding which of those to publish is /health's job.
			name: "descriptor-less providers are still counted",
			creds: []Credential{
				{ID: "x1", Provider: ProviderCursor, Token: "cur_1"},
				{ID: "y1", Provider: ProviderFactory, Token: "fact_1"},
			},
			want: map[string]int{"CURSOR": 1, "FACTORY": 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs := NewCredStore()
			cs.Load(tt.creds)

			got := cs.CountsByProvider()
			if len(got) != len(tt.want) {
				t.Fatalf("CountsByProvider() = %v, want %v", got, tt.want)
			}
			for provider, want := range tt.want {
				if got[provider] != want {
					t.Errorf("count for %s = %d, want %d (full map %v)", provider, got[provider], want, got)
				}
				// The one-pass form must agree with the per-provider form it
				// replaced, or /health starts reporting a different number
				// from the startup log.
				if n := cs.Count(ProviderType(provider)); n != want {
					t.Errorf("Count(%s) = %d but CountsByProvider says %d", provider, n, got[provider])
				}
			}
		})
	}
}

// A returned map must not be a live view of the store: a caller that ranges
// over it while a reap runs would otherwise race, and one that mutates it
// would corrupt the counts.
func TestCredStoreCountsByProviderReturnsADetachedMap(t *testing.T) {
	cs := NewCredStore()
	cs.Load([]Credential{{ID: "a1", Provider: ProviderAnthropic, Token: "sk-ant-1"}})

	got := cs.CountsByProvider()
	got["ANTHROPIC"] = 99
	got["INVENTED"] = 1

	again := cs.CountsByProvider()
	if again["ANTHROPIC"] != 1 {
		t.Errorf("mutating the returned map changed the store's answer: %v", again)
	}
	if _, ok := again["INVENTED"]; ok {
		t.Errorf("mutating the returned map added a provider: %v", again)
	}
}

// TestCredStoreSelect_ScopedToActingAgent is #2052: the store is crew-wide (one
// sidecar per crew container) and Select used to take a provider and nothing
// else, so whichever credential the round-robin happened to land on was served
// to whoever asked. For an endpoint-backed provider that is not just "whose key
// paid" — OPENAI_COMPAT takes its UPSTREAM from the credential, so a crossed
// credential sends one agent's prompts to another agent's gateway.
//
// A credential now carries the set of agent ids it was granted to; an empty set
// means the whole crew (a crew link, or a crew/workspace binding), which is the
// shape every credential had before this field existed.
func TestCredStoreSelect_ScopedToActingAgent(t *testing.T) {
	const (
		agentA = "agt_a"
		agentB = "agt_b"
		agentC = "agt_c" // in the crew, granted nothing of its own
	)
	creds := []Credential{
		// Deliberately B-first: with no agent dimension the very first Select
		// for A returns B's credential, which is the bug stated as a fixture.
		{ID: "compat-b", Provider: ProviderOpenAICompat, Token: "sk-b", BaseURL: "https://b.example/v1", AgentIDs: []string{agentB}},
		{ID: "compat-a", Provider: ProviderOpenAICompat, Token: "sk-a", BaseURL: "https://a.example/v1", AgentIDs: []string{agentA}},
		{ID: "ant-crew", Provider: ProviderAnthropic, Token: "sk-ant-crew"},
	}

	tests := []struct {
		name     string
		provider ProviderType
		agentID  string
		wantID   string // "" = expect nil
	}{
		{
			name:     "acting agent gets its own endpoint credential",
			provider: ProviderOpenAICompat,
			agentID:  agentA,
			wantID:   "compat-a",
		},
		{
			name:     "the peer gets its own, not the one loaded first",
			provider: ProviderOpenAICompat,
			agentID:  agentB,
			wantID:   "compat-b",
		},
		{
			name:     "a crew member granted neither gets nothing",
			provider: ProviderOpenAICompat,
			agentID:  agentC,
			wantID:   "",
		},
		{
			name:     "a crew-wide credential answers for agent A",
			provider: ProviderAnthropic,
			agentID:  agentA,
			wantID:   "ant-crew",
		},
		{
			name:     "and for agent B",
			provider: ProviderAnthropic,
			agentID:  agentB,
			wantID:   "ant-crew",
		},
		{
			name:     "and for a member with no grants of its own",
			provider: ProviderAnthropic,
			agentID:  agentC,
			wantID:   "ant-crew",
		},
		{
			// No identity resolved (a token-less legacy deployment). Such a
			// caller owns exactly what the whole crew owns and nothing that was
			// granted to a named agent — the least-privilege reading of "I
			// cannot tell who is asking".
			name:     "an unidentified caller sees only crew-wide credentials",
			provider: ProviderOpenAICompat,
			agentID:  "",
			wantID:   "",
		},
		{
			name:     "an unidentified caller still sees the crew-wide one",
			provider: ProviderAnthropic,
			agentID:  "",
			wantID:   "ant-crew",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cs := NewCredStore()
			cs.Load(creds)
			got := cs.Select(tt.provider, tt.agentID)
			if tt.wantID == "" {
				if got != nil {
					t.Fatalf("Select(%s, %q) = %q (upstream %s), want nil: a credential the acting agent was never granted",
						tt.provider, tt.agentID, got.ID, got.BaseURL)
				}
				return
			}
			if got == nil {
				t.Fatalf("Select(%s, %q) = nil, want %q", tt.provider, tt.agentID, tt.wantID)
			}
			if got.ID != tt.wantID {
				t.Fatalf("Select(%s, %q) = %q (upstream %s), want %q",
					tt.provider, tt.agentID, got.ID, got.BaseURL, tt.wantID)
			}
		})
	}
}

// Round-robin stays per provider (#1081) but rotates only over the credentials
// the acting agent actually holds: a tier of three where the caller owns two
// must never yield the third, and must not stall on one of its own either.
func TestCredStoreSelect_RoundRobinWithinTheActingAgentsTier(t *testing.T) {
	cs := NewCredStore()
	cs.Load([]Credential{
		{ID: "a1", Provider: ProviderOpenAICompat, Token: "sk-a1", BaseURL: "https://a1.example", AgentIDs: []string{"agt_a"}},
		{ID: "peer", Provider: ProviderOpenAICompat, Token: "sk-peer", BaseURL: "https://peer.example", AgentIDs: []string{"agt_b"}},
		{ID: "a2", Provider: ProviderOpenAICompat, Token: "sk-a2", BaseURL: "https://a2.example", AgentIDs: []string{"agt_a"}},
	})

	seen := map[string]int{}
	for i := 0; i < 6; i++ {
		got := cs.Select(ProviderOpenAICompat, "agt_a")
		if got == nil {
			t.Fatalf("call %d: Select returned nil for an agent holding two credentials", i)
		}
		seen[got.ID]++
	}
	if seen["peer"] != 0 {
		t.Errorf("the peer's credential was served %d times to agt_a", seen["peer"])
	}
	if seen["a1"] == 0 || seen["a2"] == 0 {
		t.Errorf("rotation stalled: a1=%d a2=%d, want both non-zero", seen["a1"], seen["a2"])
	}
}

// Two members calling in turn must each still rotate over their OWN keys.
//
// This is the pathology a single per-provider counter has once eligibility
// differs per agent: A holds two credentials and B one, so A's tickets all land
// on the same parity and A's second key is never reached — one member silently
// pinned to one key while its sibling is active, which is invisible until the
// pinned key hits a rate limit. The counter is therefore keyed by (provider,
// agent); the key space is the crew roster, not anything the request chooses.
func TestCredStoreSelect_RotationSurvivesInterleavedAgents(t *testing.T) {
	cs := NewCredStore()
	cs.Load([]Credential{
		{ID: "a1", Provider: ProviderAnthropic, Token: "sk-a1", AgentIDs: []string{"agt_a"}},
		{ID: "a2", Provider: ProviderAnthropic, Token: "sk-a2", AgentIDs: []string{"agt_a"}},
		{ID: "b1", Provider: ProviderAnthropic, Token: "sk-b1", AgentIDs: []string{"agt_b"}},
	})

	seen := map[string]int{}
	for i := 0; i < 8; i++ {
		a := cs.Select(ProviderAnthropic, "agt_a")
		if a == nil {
			t.Fatalf("call %d: agt_a holds two credentials and got none", i)
		}
		seen[a.ID]++
		if b := cs.Select(ProviderAnthropic, "agt_b"); b == nil || b.ID != "b1" {
			t.Fatalf("call %d: agt_b got %v, want b1", i, b)
		}
	}
	if seen["a1"] == 0 || seen["a2"] == 0 {
		t.Errorf("agt_a pinned to one key while agt_b interleaved: a1=%d a2=%d",
			seen["a1"], seen["a2"])
	}
}
