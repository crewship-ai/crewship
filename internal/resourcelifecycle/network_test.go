package resourcelifecycle

import (
	"context"
	"errors"
	"sort"
	"testing"
)

// fakeNetRuntime adds per-crew networks (#2240) to the container fake.
type fakeNetRuntime struct {
	*fakeRuntime
	nets       map[string]Network
	inUse      map[string]bool // network id -> still has endpoints
	removed    []string
	failRemove bool
	forbidden  bool
	onList     func(call int)
	listCalls  int
}

func (f *fakeNetRuntime) ListNetworks(context.Context) ([]Network, error) {
	f.listCalls++
	if f.onList != nil {
		f.onList(f.listCalls)
	}
	out := []Network{}
	for _, n := range f.nets {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
func (f *fakeNetRuntime) RemoveNetwork(_ context.Context, id string) error {
	if f.failRemove {
		return errors.New("secret daemon error")
	}
	if f.forbidden {
		return ErrNetworkForbidden
	}
	if f.inUse[id] {
		return ErrNetworkInUse
	}
	if _, ok := f.nets[id]; !ok {
		return ErrNotFound
	}
	f.removed = append(f.removed, id)
	delete(f.nets, id)
	return nil
}

func netFixture(t *testing.T) (*Controller, *fakeNetRuntime) {
	t.Helper()
	c, f := fixture(t)
	nf := &fakeNetRuntime{fakeRuntime: f, nets: map[string]Network{}, inUse: map[string]bool{}}
	c.Connect = func(context.Context) (Runtime, error) { return nf, nil }
	return c, nf
}

func crewNet(id, crew, instance string) Network {
	return Network{ID: id, CrewID: crew, InstanceID: instance, Kind: NetworkKind}
}

func TestCrewNetworkRemovedOnlyForThisInstallationsDeletedCrew(t *testing.T) {
	c, f := netFixture(t)
	unlabelled := crewNet("unlabelled", "deleted", c.InstanceID)
	unlabelled.Kind = ""
	for _, n := range []Network{
		crewNet("net-deleted", "deleted", c.InstanceID),
		crewNet("net-live", "live", c.InstanceID),
		crewNet("net-foreign", "deleted", "installation-b"),
		unlabelled,
	} {
		f.nets[n.ID] = n
	}
	c.Tick(context.Background())
	if len(f.removed) != 1 || f.removed[0] != "net-deleted" {
		t.Fatalf("removed %v, want only net-deleted", f.removed)
	}
	if s := status(t, c); s.State != "observed_clear" || s.Remaining != 0 {
		t.Fatalf("status %+v", s)
	}
}

// Containers first: while the deleted crew still has a container, its
// network is not touched, and the crew is not reported clear.
func TestCrewNetworkWaitsForContainers(t *testing.T) {
	c, f := netFixture(t)
	f.nets["net-deleted"] = crewNet("net-deleted", "deleted", c.InstanceID)
	f.items["runtime"] = item("runtime", "deleted", c.InstanceID)
	f.failStage = "remove" // the container cannot go this tick
	c.Tick(context.Background())
	if len(f.removed) != 0 {
		t.Fatalf("network removed before its crew's containers: %v", f.removed)
	}
	if s := status(t, c); s.State == "observed_clear" {
		t.Fatalf("crew reported clear with a container and a network left: %+v", s)
	}
}

// A network that still has endpoints stays pending; never forced.
func TestCrewNetworkInUseStaysPending(t *testing.T) {
	c, f := netFixture(t)
	f.nets["net-deleted"] = crewNet("net-deleted", "deleted", c.InstanceID)
	f.inUse["net-deleted"] = true
	c.Tick(context.Background())
	s := status(t, c)
	if s.State != "pending" || s.Remaining != 1 || s.Error != "" {
		t.Fatalf("in-use network: %+v, want pending with 1 remaining", s)
	}
	f.inUse["net-deleted"] = false
	c.Tick(context.Background())
	if s := status(t, c); s.State != "observed_clear" || s.Remaining != 0 {
		t.Fatalf("after endpoints left: %+v", s)
	}
}

func TestCrewNetworkRemoveFailureIsAnError(t *testing.T) {
	c, f := netFixture(t)
	f.nets["net-deleted"] = crewNet("net-deleted", "deleted", c.InstanceID)
	f.failRemove = true
	c.Tick(context.Background())
	if s := status(t, c); s.State != "error" || s.Error != "network_remove_failed" || s.Remaining != 1 {
		t.Fatalf("status %+v", s)
	}
}

// No owner inventory, no removal.
func TestCrewNetworkUnreachableDBRemovesNothing(t *testing.T) {
	c, f := netFixture(t)
	f.nets["net-deleted"] = crewNet("net-deleted", "deleted", c.InstanceID)
	if _, err := c.DB.Exec(`ALTER TABLE crews RENAME TO crews_gone`); err != nil {
		t.Fatal(err)
	}
	c.Tick(context.Background())
	if len(f.removed) != 0 {
		t.Fatalf("removed %v with no owner inventory", f.removed)
	}
}

// A runtime without network support (Apple, older wiring) keeps working.
func TestControllerWithoutNetworkRuntime(t *testing.T) {
	c, f := fixture(t)
	f.items["runtime"] = item("runtime", "deleted", c.InstanceID)
	c.Tick(context.Background())
	if s := status(t, c); s.State != "observed_clear" {
		t.Fatalf("status %+v", s)
	}
}

// A socket proxy refusing NetworkRemove is a visible error, not an endless
// pending (review of #2767).
func TestCrewNetworkForbiddenIsVisible(t *testing.T) {
	c, f := netFixture(t)
	f.nets["net-deleted"] = crewNet("net-deleted", "deleted", c.InstanceID)
	f.forbidden = true
	c.Tick(context.Background())
	if s := status(t, c); s.State != "error" || s.Error != "network_remove_forbidden" {
		t.Fatalf("status %+v", s)
	}
}

// A backup window that interrupts the network pass ends the tick with no
// writes: the recount did not run, so a status derived from Remaining would
// be a false observed_clear (review of #2767). The hook fails the pass's
// Yield while the tick's context is still live, so a fall-through would be
// able to write.
func TestCrewNetworkPassInterruptedWritesNothing(t *testing.T) {
	c, f := netFixture(t)
	f.nets["net-deleted"] = crewNet("net-deleted", "deleted", c.InstanceID)
	before := status(t, c)
	c.yieldHook = func(context.Context) error { return errors.New("backup window: tick context ended") }
	c.Tick(context.Background())
	if len(f.removed) != 0 {
		t.Fatalf("removed %v while the pass was interrupted", f.removed)
	}
	if after := status(t, c); after.State != before.State || after.Complete != before.Complete || after.Remaining != before.Remaining {
		t.Fatalf("status changed by an interrupted pass: before %+v after %+v", before, after)
	}
	var scans int
	if err := c.DB.QueryRow(`SELECT COUNT(*) FROM resource_cleanup_scans`).Scan(&scans); err != nil {
		t.Fatal(err)
	}
	if scans != 0 {
		t.Fatal("an interrupted pass wrote a scan record")
	}
}
