//go:build !clionly

package main

import (
	"strings"
	"testing"
)

// --discard-workbooks on recover acknowledges that a forced recovery replaces
// the target database and with it every workbook, which no bundle contains.
// Without --force the data directory must be empty, so the flag is refused
// before any bundle is opened.
func TestRecoverRunE_DiscardWorkbooksRequiresForce(t *testing.T) {
	flag := recoverCmd.Flags().Lookup("discard-workbooks")
	if flag == nil {
		t.Fatal("flag --discard-workbooks not registered on recover")
	}
	set := func(name, value string) {
		t.Helper()
		previous := recoverCmd.Flags().Lookup(name).Value.String()
		if err := recoverCmd.Flags().Set(name, value); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = recoverCmd.Flags().Set(name, previous) })
	}
	set("bundle", "/nonexistent/bundle.tar.zst")
	set("data-dir", t.TempDir())
	set("discard-workbooks", "true")
	err := recoverCmd.RunE(recoverCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--discard-workbooks") || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("--discard-workbooks without --force = %v, want refusal", err)
	}
}
