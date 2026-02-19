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

	first := cs.Select(ProviderAnthropic)
	if first == nil || first.ID != "c1" {
		t.Fatalf("expected c1, got %v", first)
	}

	second := cs.Select(ProviderAnthropic)
	if second == nil || second.ID != "c2" {
		t.Fatalf("expected c2, got %v", second)
	}

	// Should wrap around
	third := cs.Select(ProviderAnthropic)
	if third == nil || third.ID != "c1" {
		t.Fatalf("expected c1 (wrap), got %v", third)
	}
}

func TestCredStoreSelectEmpty(t *testing.T) {
	cs := NewCredStore()
	if cs.Select(ProviderAnthropic) != nil {
		t.Error("expected nil for empty store")
	}
}

func TestCredStoreSelectWrongProvider(t *testing.T) {
	cs := NewCredStore()
	cs.Load([]Credential{
		{ID: "c1", Provider: ProviderAnthropic, Token: "sk-ant-1"},
	})

	if cs.Select(ProviderOpenAI) != nil {
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

	cred := cs.Select(ProviderAnthropic)
	if cred == nil || cred.ID != "c2" {
		t.Fatalf("expected c2 after removing c1, got %v", cred)
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
	first := cs.Select(ProviderAnthropic)
	if first == nil || first.Priority != 1 {
		t.Fatalf("expected priority 1 cred, got %v", first)
	}
	second := cs.Select(ProviderAnthropic)
	if second == nil || second.Priority != 1 {
		t.Fatalf("expected priority 1 cred, got %v", second)
	}
	// Both selects should be from {high1, high2}
	if first.ID == second.ID {
		t.Errorf("expected round-robin between high1/high2, got same: %s", first.ID)
	}
	// Third call wraps around
	third := cs.Select(ProviderAnthropic)
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
	first := cs.Select(ProviderAnthropic)
	second := cs.Select(ProviderAnthropic)
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
			cs.Select(ProviderAnthropic)
		}()
	}
	wg.Wait()
}
