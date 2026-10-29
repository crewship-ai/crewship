package editororigin

import (
	"net/http"
	"testing"
)

// A confirmed isolation failure disables the editor; a transient one (a
// maintenance redirect for a deploy window) must not need a restart. The
// editor comes back after reenableAfterPasses consecutive passing checks,
// and stays reported as failed until then.
func TestSameHostEditorReenablesAfterConsecutivePasses(t *testing.T) {
	h, digest := sameHostFixture(t)
	index := "/_editor/b/" + digest + "/index.html"
	fail := ProxyCheck{Status: ProxyCheckFailed, Isolation: true, Problems: []string{"/_editor/b/x is redirected (HTTP 302)"}}
	pass := ProxyCheck{Status: ProxyCheckPassed}
	h.RecordCheck(studio, fail)
	for i := 1; i < 3; i++ {
		h.RecordCheck(studio, pass)
		if w := serve(h, "GET", "studio.example.test", index, frameNav...); w.Code != http.StatusServiceUnavailable {
			t.Fatalf("re-enabled after %d passes: %d", i, w.Code)
		}
	}
	h.RecordCheck(studio, fail)
	h.RecordCheck(studio, pass)
	h.RecordCheck(studio, pass)
	if w := serve(h, "GET", "studio.example.test", index, frameNav...); w.Code != http.StatusServiceUnavailable {
		t.Fatalf("a failure in between did not restart the count: %d", w.Code)
	}
	h.RecordCheck(studio, pass)
	if w := serve(h, "GET", "studio.example.test", index, frameNav...); w.Code != http.StatusOK {
		t.Fatalf("not re-enabled after three consecutive passes: %d", w.Code)
	}
	if h.Health() != HealthOK {
		t.Fatalf("health after re-enabling: %s", h.Health())
	}
}

// While disabled, the origin reports failed (never "passed and disabled").
func TestDisabledEditorReportsFailedUntilReenabled(t *testing.T) {
	h, _ := sameHostFixture(t)
	h.RecordCheck(studio, ProxyCheck{Status: ProxyCheckFailed, Isolation: true, Cause: CauseRedirect, Problems: []string{"x"}})
	h.RecordCheck(studio, ProxyCheck{Status: ProxyCheckPassed})
	d := h.Detail()
	if len(d.Origins) != 1 {
		t.Fatalf("detail %+v", d)
	}
	if o := d.Origins[0]; o.ProxyCheck != ProxyCheckFailed || !o.Disabled || o.Cause != CauseRedirect || o.ConsecutivePasses != 1 || o.Source != "configured" {
		t.Fatalf("disabled origin %+v", o)
	}
	if h.Health() != HealthDisabled {
		t.Fatalf("health %s", h.Health())
	}
}
