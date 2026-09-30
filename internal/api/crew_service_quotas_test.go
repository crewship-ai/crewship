package api

import "testing"

func TestServiceQuotaWireValidation(t *testing.T) {
	good := `[{"name":"database","image":"alpine:3","quota_enforced":true,"volumes":[{"name":"data","mount":"/data","quota_bytes":67108864}]}]`
	if err := validateServicesJSON(good); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`[{"name":"database","image":"alpine:3","quota_enforced":true,"volumes":[{"name":"data","mount":"/tmp/escape","quota_bytes":67108864}]}]`, `[{"name":"database","image":"alpine:3","quota_enforced":true,"volumes":[{"name":"data","mount":"/data","quota_bytes":-1}]}]`, `[{"name":"database","image":"alpine:3","volumes":[{"name":"data","mount":"/data","generation":1}]}]`} {
		if err := validateServicesJSON(bad); err == nil {
			t.Fatalf("invalid physical quota accepted: %s", bad)
		}
	}
}
