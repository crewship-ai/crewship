package api

import "testing"

// A saved Sheet skips its recalculation only with an engine measured to keep
// values (none listed yet) and only for bytes the server vouches for; a Word
// document and a server without the hook always recalculate.
func TestCraftNativeValidatorTrustsOnlyAMeasuredSheetEngine(t *testing.T) {
	always := func(CraftNativeDocumentProfile, []byte) bool { return true }
	unmeasured := CraftNativeDocumentProfile{Application: "gridcraft", CLISHA256: "00"}
	if craftNativeValidator(unmeasured, always).Trusted != nil {
		t.Fatal("an unmeasured engine converts without recalculating")
	}
	if craftNativeValidator(CraftNativeDocumentProfile{Application: "wordcraft", CLISHA256: "c2959f753baa75f69a70b2d074ba285e58466bb849fcb98c057819ae284c1000"}, always).Trusted != nil {
		t.Fatal("a Word document is trusted")
	}
	if craftNativeValidator(unmeasured, nil).Trusted != nil {
		t.Fatal("trusted without a hook")
	}
}

// With the measured engine, the validator asks the server about each saved
// document: a snapshot it vouches for converts without recalculating, the
// browser's bytes (which no session wrote) are recalculated in full.
func TestCraftNativeValidatorRecalculatesWhatTheServerDidNotCompute(t *testing.T) {
	own := []byte(`{"computed":"by this server"}`)
	var asked []CraftNativeDocumentProfile
	trusted := func(p CraftNativeDocumentProfile, b []byte) bool {
		asked = append(asked, p)
		return string(b) == string(own)
	}
	p := CraftNativeDocumentProfile{Application: "gridcraft", CLISHA256: "52acf14ac2a4c7386a7333f9d18e548ac24b089c02afd47c71ace7e1540d0bc3"}
	v := craftNativeValidator(p, trusted)
	if v.Trusted == nil {
		t.Fatal("the measured engine still recalculates the server's own snapshot")
	}
	if !v.Trusted(own) {
		t.Fatal("the server's own snapshot is not trusted")
	}
	if v.Trusted([]byte(`{"computed":"by a browser"}`)) {
		t.Fatal("a browser's bytes skip the recalculation")
	}
	if len(asked) != 2 || asked[0].CLISHA256 != p.CLISHA256 {
		t.Fatalf("asked about the wrong profile: %+v", asked)
	}
}
