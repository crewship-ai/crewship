package runtimestage

import "os"

// Windows host ACL custody needs separate qualification before publishing a
// trusted runtime companion. Packaging a Linux client alone is not that proof.
func agentCLINoFollowFlag() int               { return 0 }
func safeAgentCLIFile(os.FileInfo, bool) bool { return false }
func safeAgentCLIDirectory(os.FileInfo) bool  { return false }
