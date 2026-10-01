package serviceconfig

import (
	"strings"
	"testing"
)

func TestQuotaTransition(t *testing.T) {
	vol := func(bytes, gen string) string {
		v := `{"name":"data","mount":"/data","quota_bytes":` + bytes
		if gen != "" {
			v += `,"generation":` + gen
		}
		return `[{"name":"database","image":"postgres:16","quota_enforced":true,"volumes":[` + v + `}]}]`
	}
	cases := []struct {
		name, previous, next string
		wantErr              string
	}{
		{name: "unchanged", previous: vol("67108864", "1"), next: vol("67108864", "1")},
		{name: "implicit generation one", previous: vol("67108864", ""), next: vol("67108864", "1")},
		{name: "capacity change with bump", previous: vol("67108864", "1"), next: vol("134217728", "2")},
		{name: "capacity change without bump", previous: vol("67108864", "1"), next: vol("134217728", "1"), wantErr: "generation"},
		{name: "capacity change with implicit generation", previous: vol("67108864", ""), next: vol("134217728", ""), wantErr: "generation 2"},
		{name: "new service", previous: `[]`, next: vol("67108864", "")},
		{name: "first opt-in", previous: `[{"name":"database","image":"postgres:16","volumes":[{"name":"data","mount":"/data"}]}]`, next: vol("67108864", "")},
		{name: "no previous config", previous: "", next: vol("67108864", "")},
		{name: "redacted previous config", previous: Redacted, next: vol("67108864", "")},
		{name: "renamed volume is a new volume", previous: vol("67108864", "1"), next: strings.Replace(vol("134217728", "1"), `"name":"data"`, `"name":"pgdata"`, 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := QuotaTransition(tc.previous, tc.next)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want mention of %q", err, tc.wantErr)
			}
		})
	}
}
