package backup

import "testing"

// BenchmarkNewRemapCUID fires during backup restore when IDs need remapping
// to avoid collisions with existing rows. For large restores (thousands of
// rows with FK conflicts) it runs at CPU pace and was paying three allocation
// chains per call: `base36` prepending bytes, `fmt.Sprintf` formatting, and
// `hex.EncodeToString(b)[:8]` encoding 16 chars to keep 8.
func BenchmarkNewRemapCUID(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = newRemapCUID()
	}
}
