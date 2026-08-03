package memory

import "fmt"

// The ownership contract of a crew's memory tree, in one place.
//
// Three processes write into `.memory` and they are not the same user:
//
//   - the AGENT, uid 1001, through its own tools
//   - the SIDECAR, uid 1002 — deliberately not 1001, so the agent
//     process cannot read /proc/<sidecar>/mem and lift credentials off
//     its heap (orchestrator/exec_sidecar.go)
//   - the HOST, through restore and memory import, from outside the
//     container entirely
//
// What lets three identities share one tree is group 1002 plus setgid
// directories: whoever creates a file, the group is 1002 and the mode
// is group-writable, so the next writer is not locked out.
//
// That contract used to be spelled out inline in the docker provider's
// container-start command, and nowhere else — so the restore path,
// which needs the same guarantee, could only discover it by failing
// (#1746). It lives here now because it is a property of the memory
// tree, not of any one thing that writes to it.
const (
	// MemoryOwnerUser is the uid:gid a memory entry should carry: the
	// agent owns it, the shared memory group can write it.
	MemoryOwnerUser = "1001:1002"
	// MemoryGroup is the gid every writer into the tree shares.
	MemoryGroup = "1002"
	// MemoryDirMode is setgid + group-writable, which is what makes
	// group inheritance work for anything created later.
	MemoryDirMode = "2775"
)

// MemoryOwnershipCmd returns the shell that establishes the contract
// under crewPath. crewPath must already be quoted by the caller, which
// knows whether it is naming a host path or an in-container one.
//
// It does NOT change file ownership — at container start the enclosing
// chown has just run and everything is 1001 already. Restore and import
// come to a tree that has been written to since, and need
// MemoryReclaimOwnershipCmd instead.
func MemoryOwnershipCmd(crewPath string) string {
	return fmt.Sprintf(
		`find %s -name .memory -type d -exec chgrp -R %s {} +`+
			` ; find %s -name .memory -type d -exec chmod %s {} +`+
			` ; find %s -path '*/.memory/*' -type f -exec chmod g+rw {} +`,
		crewPath, MemoryGroup, crewPath, MemoryDirMode, crewPath)
}

// MemoryReclaimOwnershipCmd is the contract applied to a tree that has
// already been written to by the sidecar.
//
// A file the sidecar created is owned by 1002 at mode 0644: group 1002
// can read it and nobody else can write it. A host-side writer
// extracting as the agent is then refused — which is how a crew whose
// agents had used their memory became impossible to restore.
//
// Handing those entries back to 1001:1002 restores the shared-write
// property the tree is built on: the agent owns them, the sidecar keeps
// write through the group, and the next restore does not have to ask
// anyone for a chown.
//
// Requires root, which is what makes it a deliberate step rather than
// something any writer does on the way past.
func MemoryReclaimOwnershipCmd(crewPath string) string {
	return fmt.Sprintf(
		`find %s -name .memory -type d -exec chown -R %s {} +`+
			` ; %s`,
		crewPath, MemoryOwnerUser, MemoryOwnershipCmd(crewPath))
}
