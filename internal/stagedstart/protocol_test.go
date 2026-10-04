package stagedstart

import (
	"sync"
	"testing"
)

func TestBootNonceAndOneUseAdmission(t *testing.T) {
	s := State{Nonce: "current-boot", Phase: "staging"}
	for _, r := range []Request{{Operation: "seal", Nonce: "old-boot"}, {Operation: "consume", Nonce: s.Nonce}, {Operation: "exec", Nonce: s.Nonce}} {
		if out := s.Apply(r, 1001); out.Error == "" {
			t.Fatalf("unsealed admission: %+v", r)
		}
	}
	if s.Apply(Request{Operation: "seal", Nonce: s.Nonce}, 1001).Error == "" {
		t.Fatal("workload sealed itself")
	}
	if s.Apply(Request{Operation: "seal", Nonce: s.Nonce}, 1002).Error != "" {
		t.Fatal("trusted seal denied")
	}
	if s.Apply(Request{Operation: "reserve", Nonce: s.Nonce}, 1002).Error != "" {
		t.Fatal("trusted reservation denied")
	}
	var mu sync.Mutex
	var wg sync.WaitGroup
	admitted := 0
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			mu.Lock()
			defer mu.Unlock()
			if s.Apply(Request{Operation: "consume", Nonce: s.Nonce}, 1001).Error == "" {
				admitted++
			}
		}()
	}
	wg.Wait()
	if admitted != 1 || s.Phase != "bootstrapping" {
		t.Fatalf("bootstrap admissions=%d phase=%s", admitted, s.Phase)
	}
	if s.Apply(Request{Operation: "seal", Nonce: s.Nonce}, 1002).Error == "" {
		t.Fatal("unknown outcome reset consumed permit")
	}
	if s.Apply(Request{Operation: "exec", Nonce: s.Nonce}, 1001).Error == "" {
		t.Fatal("unknown outcome marked ready")
	}
}

func TestRestartAndFailedBootstrapRejectReplay(t *testing.T) {
	for _, phase := range []string{"staging", "sealed", "reserved", "failed", "bootstrapping", "ready"} {
		s := State{Nonce: "new-boot", Phase: phase}
		for _, op := range []string{"seal", "consume", "exec"} {
			if s.Apply(Request{Operation: op, Nonce: "old-boot"}, 1002).Error == "" {
				t.Fatalf("stale %s in %s", op, phase)
			}
		}
		if phase != "ready" && s.Apply(Request{Operation: "exec", Nonce: s.Nonce}, 1001).Error == "" {
			t.Fatalf("exec in %s", phase)
		}
	}
}
