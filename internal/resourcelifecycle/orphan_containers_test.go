package resourcelifecycle

import (
	"context"
	"errors"
	"testing"
	"time"
)

type orphanTestRuntime struct {
	*fakeRuntime
	reasons     map[string]string
	unreachable bool
}

func (r *orphanTestRuntime) OrphanRetentionReason(_ context.Context, c Container) (string, error) {
	if r.unreachable {
		return "", errors.New("unavailable")
	}
	if _, ok := r.items[c.ID]; !ok {
		return "", ErrNotFound
	}
	return r.reasons[c.ID], nil
}
func orphanFixture(t *testing.T) (*Controller, *orphanTestRuntime, *time.Time) {
	t.Helper()
	c, f := fixture(t)
	r := &orphanTestRuntime{fakeRuntime: f, reasons: map[string]string{}}
	now := time.Now()
	c.Now = func() time.Time { return now }
	c.Connect = func(context.Context) (Runtime, error) { return r, nil }
	f.items["candidate"] = Container{ID: "candidate", InstanceID: c.InstanceID, CrewID: "absent", Kind: "crew"}
	return c, r, &now
}
func TestMissingCrewContainerGraceAndProtection(t *testing.T) {
	t.Setenv("CREWSHIP_IMAGE_GC", "true")
	c, r, now := orphanFixture(t)
	for id, crew := range map[string]string{"live": "live", "app": "absent", "mounted": "absent", "running": "absent", "published": "absent", "foreign": "absent", "unowned": "absent", "empty-crew": "", "image-gone": "absent"} {
		x := Container{ID: id, CrewID: crew, InstanceID: c.InstanceID, Kind: "crew"}
		switch id {
		case "image-gone":
			r.reasons[id] = "missing-image"
		case "foreign":
			x.InstanceID = "another"
		case "unowned":
			x.InstanceID = ""
		case "app", "mounted", "running", "published":
			r.reasons[id] = id
		}
		r.items[id] = x
	}
	// Model a restore of a DB predating the crew. Observations live with
	// the server, not in the restored file.
	c.Tick(t.Context())
	for range 47 {
		*now = now.Add(orphanContainerInterval)
		c.Tick(t.Context())
	}
	if len(r.calls) != 0 {
		t.Fatalf("during grace: %v", r.calls)
	}
	*now = now.Add(orphanContainerInterval)
	c.Tick(t.Context())
	if len(r.calls) != 1 || r.calls[0] != "remove:candidate" || len(r.items) != 9 {
		t.Fatalf("removals %v; survivors %d", r.calls, len(r.items))
	}
}
func TestMissingCrewContainerIncompleteOrBusyRetains(t *testing.T) {
	for _, mode := range []string{"disabled", "invalid", "gap", "restart", "DB restore", "image use", "provisioning preflight", "active restore", "incomplete inventory", "inspection error", "database error", "completed restore", "acquired app"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("CREWSHIP_IMAGE_GC", "true")
			c, r, now := orphanFixture(t)
			c.Tick(t.Context())
			for range 47 {
				*now = now.Add(orphanContainerInterval)
				c.Tick(t.Context())
			}
			switch mode {
			case "disabled":
				t.Setenv("CREWSHIP_IMAGE_GC", "false")
			case "invalid":
				t.Setenv("CREWSHIP_IMAGE_GC", "1?")
			case "gap":
				*now = now.Add(3 * time.Hour)
			case "restart":
				c = &Controller{DB: c.DB, InstanceID: c.InstanceID, Connect: c.Connect, Now: c.Now}
			case "DB restore":
				if _, err := c.DB.Exec("INSERT INTO crews(id) VALUES('absent')"); err != nil {
					t.Fatal(err)
				}
			case "image use":
				release, err := Images(c.DB).UseImages(t.Context(), "active")
				if err != nil {
					t.Fatal(err)
				}
				defer release()
			case "provisioning preflight":
				defer Images(c.DB).Provisioning()()
			case "completed restore":
				Images(c.DB).Restore()()
			case "active restore":
				defer Images(c.DB).Use()()
			case "incomplete inventory":
				r.failList = r.lists + 1
			case "inspection error":
				r.unreachable = true
			case "database error":
				if _, err := c.DB.Exec("ALTER TABLE crews RENAME TO unavailable"); err != nil {
					t.Fatal(err)
				}
			case "acquired app":
				r.reasons["candidate"] = "application"
			}
			*now = now.Add(orphanContainerInterval)
			c.Tick(t.Context())
			if len(r.calls) != 0 {
				t.Fatalf("removed with %s: %v", mode, r.calls)
			}
		})
	}
}
