package mooring

import (
	"fmt"
	"slices"
	"testing"
)

func TestOriginInventoryDataCanonicalBoundsAndDigest(t *testing.T) {
	input := []OriginClockData{{"0", "1"}, {"2", "4294967295"}, {"9007199254740991", "17"}}
	data, err := NormalizeOriginInventoryData(input)
	if err != nil {
		t.Fatal(err)
	}
	// Independent fixed big-endian tuple vector, not a JSON-number encoding.
	const expected = "sha256:273010a291019a9ca94e2e6ad975fea7a3b08c9d0a07cc485bd448e646959eaa"
	if data.Digest() != expected || !slices.Equal(data.Entries(), input) {
		t.Fatal("canonical inventory correlation changed")
	}
	input[0].Origin = "88"
	copy := data.Entries()
	copy[1].Clock = "3"
	if data.Entries()[0].Origin != "0" || data.Entries()[1].Clock != "4294967295" || data.Digest() != expected {
		t.Fatal("caller rewrote normalized inventory")
	}
	changed, err := NormalizeOriginInventoryData([]OriginClockData{{"0", "2"}, {"2", "4294967295"}, {"9007199254740991", "17"}})
	if err != nil || changed.Digest() == expected {
		t.Fatal("clock change lost correlation", err)
	}
}

func TestOriginInventoryDataRejectsNoncanonicalAndUnboundedInput(t *testing.T) {
	for _, value := range []string{"", "00", "01", "+1", "-1", " 1", "1 ", "1.0", "1e0", "١", "9007199254740992", "18446744073709551616"} {
		t.Run("origin/"+value, func(t *testing.T) {
			if result, err := NormalizeOriginInventoryData([]OriginClockData{{value, "1"}}); err == nil || result.Digest() != "" {
				t.Fatal("invalid origin normalized")
			}
		})
	}
	for _, value := range []string{"", "0", "01", "+1", "-1", "1.0", "4294967296", "18446744073709551616"} {
		t.Run("clock/"+value, func(t *testing.T) {
			if _, err := NormalizeOriginInventoryData([]OriginClockData{{"1", value}}); err == nil {
				t.Fatal("invalid allocated clock normalized")
			}
		})
	}
	for _, input := range [][]OriginClockData{
		{{"2", "1"}, {"2", "2"}},
		{{"10", "1"}, {"2", "1"}},
		make([]OriginClockData, MaxOriginInventoryEntries+1),
	} {
		if _, err := NormalizeOriginInventoryData(input); err == nil {
			t.Fatal("duplicate, unsorted or oversized inventory normalized")
		}
	}
}

func TestOriginCandidateIncludesUnusedReservationsAndBaselineZero(t *testing.T) {
	baseline, err := NormalizeOriginInventoryData([]OriginClockData{{"0", "1"}, {"2", "4"}, {"9007199254740991", "1"}})
	if err != nil {
		t.Fatal(err)
	}
	// Origins 1 and 3 have no committed structs in the baseline. They still
	// exclude unused/rejected/closed issued sources, including after GC.
	if candidate, err := LeastUnusedOriginData(baseline, []string{"1", "2", "3"}); err != nil || candidate != "4" {
		t.Fatal("issued but uncommitted source was reusable", candidate, err)
	}
	if _, err := LeastUnusedOriginData(OriginInventoryData{}, nil); err == nil {
		t.Fatal("zero value was mistaken for a normalized empty baseline")
	}
	empty, err := NormalizeOriginInventoryData(nil)
	if err != nil {
		t.Fatal(err)
	}
	// This is only a DATA candidate. Empty normalized data is not checkpoint
	// evidence and must not become an issuance or authority constructor.
	if candidate, err := LeastUnusedOriginData(empty, nil); err != nil || candidate != "1" {
		t.Fatal(candidate, err)
	}
	for _, issued := range [][]string{{"0"}, {"01"}, {"9007199254740992"}, {"1", "1"}, {"10", "2"}, make([]string, MaxOriginInventoryEntries+1)} {
		if _, err := LeastUnusedOriginData(empty, issued); err == nil {
			t.Fatal("invalid exclusion list accepted")
		}
	}
}

func TestOriginCandidateCapacityCountsDistinctUnionAndNeverEvicts(t *testing.T) {
	entries := make([]OriginClockData, MaxOriginInventoryEntries)
	issued := make([]string, MaxOriginInventoryEntries)
	for i := range entries {
		entries[i] = OriginClockData{fmt.Sprint(i), "1"}
		issued[i] = fmt.Sprint(i + 1)
	}
	full, err := NormalizeOriginInventoryData(entries)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LeastUnusedOriginData(full, nil); err == nil {
		t.Fatal("full baseline silently evicted an occupied origin")
	}
	partial, err := NormalizeOriginInventoryData(entries[:len(entries)-1])
	if err != nil {
		t.Fatal(err)
	}
	if candidate, err := LeastUnusedOriginData(partial, issued[:len(issued)-2]); err != nil || candidate != "4095" {
		t.Fatal("overlapping baseline/issued entries double counted", candidate, err)
	}
	if _, err := LeastUnusedOriginData(partial, []string{"9007199254740991"}); err == nil {
		t.Fatal("high tombstone outside checkpoint was omitted from capacity")
	}
	if _, err := LeastUnusedOriginData(partial, issued); err == nil {
		t.Fatal("union overflow accepted")
	}
}
