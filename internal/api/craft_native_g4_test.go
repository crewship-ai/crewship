package api

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

// B7: a request refused at storage admission (no engine call yet) must stay
// retryable under the same request ID instead of becoming a permanent
// "engine" failure.
func TestCraftNativeAgentToolStorageBusyRequestStaysRetryable(t *testing.T) {
	for _, op := range []string{"apply", "propose"} {
		t.Run(op, func(t *testing.T) {
			f, _, _, call, calls := nativeAgentToolFixture(t)
			gen := ""
			if op == "apply" {
				acquired := call(`{"operation":"acquire"}`)
				if acquired.Code != 200 {
					t.Fatal(acquired.Code, acquired.Body)
				}
				gen = fmt.Sprintf(`"generation":%q,`, jsonField(t, acquired.Body.Bytes(), "generation"))
			}
			expiry := time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
			for i := 0; i < 4; i++ {
				mustExec(t, f.rig.db, `INSERT INTO artifact_storage_reservations(id,workspace_id,page_id,bytes,created_at,expires_at) VALUES(?,?,NULL,1,?,?)`, fmt.Sprintf("busy-%d", i), f.rig.wsID, expiry, expiry)
			}
			packet := fmt.Sprintf(`{"operation":%q,%s"base_version":"1","request_id":"retry-after-busy","commands":[{"command":"text.insert","params":{"text":"x"}}]}`, op, gen)
			busy := call(packet)
			if busy.Code == 200 || *calls != 0 {
				t.Fatal("expected storage refusal before the engine", busy.Code, busy.Body, *calls)
			}
			mustExec(t, f.rig.db, `DELETE FROM artifact_storage_reservations`)
			retry := call(packet)
			if retry.Code/100 != 2 || *calls != 1 {
				t.Fatal("retry after storage busy must run the engine once", retry.Code, retry.Body, *calls)
			}
		})
	}
}

// B8: run with -race. The lease-renewal goroutine reads the apply context
// while the request goroutine rebinds it to the storage upload.
func TestCraftNativeAgentApplyRenewalDoesNotRaceWithUploadContext(t *testing.T) {
	_, _, _, call, _ := nativeAgentToolFixture(t)
	acquired := call(`{"operation":"acquire"}`)
	if acquired.Code != 200 {
		t.Fatal(acquired.Code, acquired.Body)
	}
	gen := jsonField(t, acquired.Body.Bytes(), "generation")
	for i := 0; i < 5; i++ {
		packet := fmt.Sprintf(`{"operation":"apply","generation":%q,"base_version":"1","request_id":"race-%d","commands":[{"command":"text.insert","params":{"text":"x"}}]}`, gen, i)
		call(packet)
	}
}

// B6: a flood of fan-outs against a stopped hub must not leave an unbounded
// number of goroutines waiting.
func TestPublishUserAccessRecheckBoundsWaitingFanouts(t *testing.T) {
	hub := ws.NewHub(newTestLogger(), nil, ws.NopValidatorForTests, ws.NopSessionsForTests) // never Run: broadcast queue fills and blocks
	// Every admitted fan-out holds a slot until the send timeout against the
	// stopped hub; at 5 s that cost the shard 85 s (#4710).
	// Registered first so it runs after the wait below: waiters read it later.
	recheckInFlight.Wait() // fan-outs left by earlier tests must not read the write below
	saved := recheckSendTimeout
	t.Cleanup(func() { recheckSendTimeout = saved })
	recheckSendTimeout = 200 * time.Millisecond
	t.Cleanup(func() { recheckInFlight.Wait() })
	users := make([]string, 400) // more than the hub queue holds, so each fan-out blocks
	for i := range users {
		users[i] = fmt.Sprintf("u%d", i)
	}
	before := recheckPending.Load()
	var wg sync.WaitGroup
	for i := 0; i < 600; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); publishUserAccessRecheck(hub, nil, users, map[string]any{}) }()
	}
	wg.Wait()
	if got := recheckPending.Load() - before; got > int64(cap(recheckSlots))+recheckMaxWaiting {
		t.Fatalf("%d fan-outs pending, want at most %d", got, int64(cap(recheckSlots))+recheckMaxWaiting)
	}
}

func jsonField(t *testing.T, body []byte, key string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	s, _ := m[key].(string)
	return s
}
