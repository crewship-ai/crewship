package docker

import (
	"errors"
	"net/netip"
	"testing"

	"github.com/crewship-ai/crewship/internal/quota"
)

// A new quota volume is formatted for the user the service image runs as.
// Only numeric users can be resolved without reading the image's passwd;
// names keep the root-owned default (the image entrypoint chowns, as the
// official postgres/redis/mysql images do).
func TestQuotaVolumeOwnerFollowsImageUser(t *testing.T) {
	cases := []struct {
		user string
		want quota.Owner
	}{
		{user: "", want: quota.Owner{}},
		{user: "root", want: quota.Owner{}},
		{user: "0", want: quota.Owner{}},
		{user: "0:0", want: quota.Owner{}},
		{user: "1001", want: quota.Owner{UID: 1001, Set: true}},
		{user: "999:998", want: quota.Owner{UID: 999, GID: 998, Set: true}},
		{user: "postgres", want: quota.Owner{}},
		{user: "1001:staff", want: quota.Owner{UID: 1001, Set: true}},
	}
	for _, tc := range cases {
		t.Run("user="+tc.user, func(t *testing.T) {
			daemon := newFakeQuotaDaemon(t)
			daemon.imageConfig = map[string]any{"User": tc.user, "Volumes": map[string]any{"/data": map[string]any{}}}
			catalog := &fakeQuotaCatalog{}
			svc := quotaTestService()
			p := newCovProvider(t, Config{QuotaCatalog: catalog}, daemon.ServeHTTP)
			if _, err := p.ensureSidecar(t.Context(), covCrewID, "alpha", p.crewNetworkFor(covCrewID, "alpha"), netip.Addr{}, &svc); err != nil {
				t.Fatal(err)
			}
			if len(catalog.owners) != 1 || catalog.owners[0] != tc.want {
				t.Fatalf("owner %+v, want %+v", catalog.owners, tc.want)
			}
		})
	}
}

// An image-declared anonymous volume a quota service did not classify is
// refused before any disk is allocated.
func TestQuotaServiceRefusesUnclassifiedImageVolumeBeforeAllocation(t *testing.T) {
	daemon := newFakeQuotaDaemon(t)
	daemon.imageConfig = map[string]any{"Volumes": map[string]any{"/data": map[string]any{}, "/var/lib/extra": map[string]any{}}}
	catalog := &fakeQuotaCatalog{}
	svc := quotaTestService()
	p := newCovProvider(t, Config{QuotaCatalog: catalog}, daemon.ServeHTTP)
	if _, err := p.ensureSidecar(t.Context(), covCrewID, "alpha", p.crewNetworkFor(covCrewID, "alpha"), netip.Addr{}, &svc); !errors.Is(err, quota.ErrDenied) {
		t.Fatalf("unclassified image volume admitted: %v", err)
	}
	if len(catalog.owners) != 0 || daemon.mutatingCalls() != 0 {
		t.Fatal("refused service still allocated disk")
	}
}
