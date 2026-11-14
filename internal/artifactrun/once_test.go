package artifactrun

import (
	"fmt"
	"testing"
	"time"
)

func TestOnce_RemembersAndStaysBounded(t *testing.T) {
	var o Once
	if !o.First("a") || o.First("a") {
		t.Fatal("a key is first exactly once")
	}
	o.Forget("a")
	if !o.First("a") {
		t.Fatal("a forgotten key is first again")
	}
	for i := 0; i < onceLimit*2; i++ {
		o.First(fmt.Sprint("k", i))
	}
	if n := o.Len(); n > onceLimit {
		t.Fatalf("Once grew to %d, limit %d", n, onceLimit)
	}
}

// A key is remembered for MaxTTL, the longest a run lives, and then is
// first again; past the limit, expired keys go before anything else.
func TestOnce_ForgetsAfterTheLongestRun(t *testing.T) {
	at := time.Now()
	o := Once{now: func() time.Time { return at }}
	o.First("run")
	at = at.Add(MaxTTL - time.Second)
	if o.First("run") {
		t.Fatal("a key was forgotten before MaxTTL")
	}
	at = at.Add(2 * time.Second)
	if !o.First("run") {
		t.Fatal("a key was remembered past MaxTTL")
	}
	for i := 0; i < onceLimit-1; i++ {
		o.First(fmt.Sprint("old", i))
	}
	at = at.Add(MaxTTL)
	o.First("fresh")
	if o.First("fresh") || o.Len() != 1 {
		t.Fatalf("past the limit the expired keys were not dropped first: %d kept", o.Len())
	}
}
