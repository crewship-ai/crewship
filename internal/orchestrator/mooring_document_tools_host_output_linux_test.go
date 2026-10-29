//go:build linux

package orchestrator

import "testing"

// This uses a synthetic host-capable provider; it performs no protected host
// capture and requires no special host UID. The normal Go suite runs it.
func TestPrivateRunUsesDirectExecWithGlobalRetainedOutputConfigured(t *testing.T) {
	p := &runAgentHostProvider{covContainer: covNewRunContainer(covRunOpts{})}
	o := New(p, newMemState(), covQuietLogger())
	o.SetHostOutputRoot(t.TempDir())
	req := covRunReq().WithStrictContentPrivacy()
	starts := 0
	req.OnExecStarted = func(string) { starts++ }
	if err := o.RunAgent(t.Context(), req, nil); err != nil {
		t.Fatal(err)
	}
	if starts != 1 || len(p.prepared) != 0 {
		t.Fatalf("private exec starts=%d protected captures=%d", starts, len(p.prepared))
	}
}
