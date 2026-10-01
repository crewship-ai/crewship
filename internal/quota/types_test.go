package quota

import "testing"

func TestKeyAndPhysicalQuotaBounds(t *testing.T) {
	good := Key{"crew1", "database", "data", 1}
	if !good.valid() || good.id() == (Key{"crew2", "database", "data", 1}).id() {
		t.Fatal("unstable or shared key")
	}
	for _, bad := range []Key{{"../host", "database", "data", 1}, {"crew", "/etc", "data", 1}, {"crew", "database", "a/b", 1}, {"crew", "database", "data", 0}} {
		if bad.valid() {
			t.Fatalf("caller path accepted: %+v", bad)
		}
	}
	for _, bad := range []int64{0, MinBytes - 1, MaxBytes + 1, MinBytes + 1} {
		if validBytes(bad) {
			t.Fatalf("invalid capacity accepted %d", bad)
		}
	}
}
