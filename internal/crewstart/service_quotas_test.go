package crewstart

import "testing"

func TestPhysicalQuotaWireRoundTrip(t *testing.T) {
	input := `[{"name":"database","image":"alpine:3","quota_enforced":true,"volumes":[{"name":"data","mount":"/data","quota_bytes":67108864,"generation":2}]}]`
	services, err := DecodeServices(input, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 1 || !services[0].QuotaEnforced || services[0].Volumes[0].QuotaBytes != 64<<20 || services[0].Volumes[0].Generation != 2 {
		t.Fatalf("quota classification lost: %+v", services)
	}
	for _, input := range []string{`[{"name":"database","image":"alpine:3","quota_enforced":true,"volumes":[{"name":"data","mount":"/data"}]}]`, `[{"name":"database","image":"alpine:3","volumes":[{"name":"data","mount":"/data","quota_bytes":67108864}]}]`} {
		if _, err := DecodeServices(input, nil); err == nil {
			t.Fatal("malformed quota policy decoded")
		}
	}
}
