package crewfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeDest(t *testing.T) {
	cases := []struct {
		src, dest, want string
		ok              bool
	}{
		{"scripts/x.sh", "", "shared/x.sh", true},                          // default = shared/<base>
		{"x.sh", "shared/scripts/x.sh", "shared/scripts/x.sh", true},       // explicit
		{"x.sh", "/crew/shared/scripts/x.sh", "shared/scripts/x.sh", true}, // /crew/ spelling
		{"x.sh", "../../etc/passwd", "", false},                            // traversal
		{"x.sh", "output/x.sh", "", false},                                 // outside shared/
		{"x.sh", "shared", "", false},                                      // bare shared dir
		{"", "", "", false},                                                // no src, no dest
	}
	for _, tc := range cases {
		got, err := NormalizeDest(tc.src, tc.dest)
		if (err == nil) != tc.ok || got != tc.want {
			t.Errorf("NormalizeDest(%q,%q) = (%q, err=%v), want (%q, ok=%v)",
				tc.src, tc.dest, got, err, tc.want, tc.ok)
		}
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "s.sh"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	data, err := Load(dir, "s.sh")
	if err != nil || string(data) != "hi" {
		t.Fatalf("Load = %q, %v", data, err)
	}
	if _, err := Load(dir, "missing.sh"); err == nil {
		t.Error("Load of missing src must error")
	}
	if _, err := Load(dir, ""); err == nil {
		t.Error("Load of empty src must error")
	}
}
