package chatbridge

import "testing"

// BenchmarkGenerateMsgID exercises the message ID generator that fires
// roughly 3× per chat round-trip (user-message, run-id, assistant-reply).
// Under live chat workloads this compounds quickly.
func BenchmarkGenerateMsgID(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = generateMsgID()
	}
}
