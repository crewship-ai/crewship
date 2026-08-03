package memory

import (
	"strings"
	"testing"
)

// The contract the container-start sweep has always applied. Pinned
// verbatim because provider/docker now delegates here: a change to this
// string changes how every crew's memory tree is set up.
func TestMemoryOwnershipCmdIsTheEstablishedContract(t *testing.T) {
	got := MemoryOwnershipCmd(`"/mnt/crew"`)
	want := `find "/mnt/crew" -name .memory -type d -exec chgrp -R 1002 {} +` +
		` ; find "/mnt/crew" -name .memory -type d -exec chmod 2775 {} +` +
		` ; find "/mnt/crew" -path '*/.memory/*' -type f -exec chmod g+rw {} +`
	if got != want {
		t.Errorf("MemoryOwnershipCmd changed:\n got %s\nwant %s", got, want)
	}
}

// Reclaim is a superset: it hands entries back to the agent FIRST, then
// applies the same contract. Without the chown a sidecar-written file
// stays owned by 1002 and a host writer extracting as the agent cannot
// replace it — the state that made a crew unrestorable (#1746).
func TestMemoryReclaimOwnershipCmdChownsBeforeApplyingTheContract(t *testing.T) {
	got := MemoryReclaimOwnershipCmd(`"/crew"`)
	chown := strings.Index(got, "chown -R "+MemoryOwnerUser)
	chgrp := strings.Index(got, "chgrp -R "+MemoryGroup)
	if chown < 0 {
		t.Fatalf("no chown to %s: %s", MemoryOwnerUser, got)
	}
	if chgrp < 0 {
		t.Fatalf("the base contract is not applied: %s", got)
	}
	if chown > chgrp {
		t.Error("chown must come first; applying modes to files another uid owns is EPERM")
	}
	if !strings.Contains(got, MemoryOwnershipCmd(`"/crew"`)) {
		t.Error("reclaim does not carry the base contract verbatim — the two would drift")
	}
}

// The agent owns, the shared group writes. Both halves are load-bearing:
// owner alone locks the sidecar out, group alone locks the host out.
func TestMemoryOwnerUserPairsAgentWithSharedGroup(t *testing.T) {
	if MemoryOwnerUser != "1001:"+MemoryGroup {
		t.Errorf("MemoryOwnerUser = %q, want the agent uid with group %s", MemoryOwnerUser, MemoryGroup)
	}
}
