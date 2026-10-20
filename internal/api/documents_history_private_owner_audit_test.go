package api

import "testing"

func TestDocumentPrivateOwnerAuditHistoryCustodyFence(t *testing.T) {
	assertDocumentPrivateOwnerAuditCustodyFence(t, "checkpoints", (*PageHandler).ListDocumentCheckpoints)
	assertDocumentPrivateOwnerAuditCustodyFence(t, "checkpoint-read", (*PageHandler).ReadDocumentCheckpoint)
}
