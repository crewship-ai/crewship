package resourcelifecycle

import (
	"context"
	"errors"
	"testing"
)

type fakeBindVolumeRuntime struct {
	*fakeRuntime
	instance string
	limit    int
	calls    int
	err      error
}

func (f *fakeBindVolumeRuntime) ReapBindVolumes(_ context.Context, instance string, limit int) error {
	f.calls++
	f.instance, f.limit = instance, limit
	if len(f.items) != 0 {
		return errors.New("reaper ran before container removal")
	}
	return f.err
}

func TestControllerReapsBindRecordsAndRetriesFailure(t *testing.T) {
	c, f := fixture(t)
	c.Batch = 7
	f.items["runtime"] = item("runtime", "deleted", c.InstanceID)
	vr := &fakeBindVolumeRuntime{fakeRuntime: f, err: errors.New("secret daemon failure")}
	c.Connect = func(context.Context) (Runtime, error) { return vr, nil }
	c.Tick(context.Background())
	if vr.calls != 1 || vr.instance != c.InstanceID || vr.limit != 7 {
		t.Fatalf("reaper %+v", vr)
	}
	if s := status(t, c); s.State != "unknown" || s.Complete || s.Error != "bind_volume_cleanup_failed" {
		t.Fatalf("failure status %+v", s)
	}
	vr.err = nil
	c.Tick(context.Background())
	if vr.calls != 2 {
		t.Fatal("failed reaper was not retried")
	}
	if s := status(t, c); s.State != "observed_clear" || !s.Complete {
		t.Fatalf("retry status %+v", s)
	}
	c.InstanceID = ""
	c.Tick(context.Background())
	if vr.calls != 2 {
		t.Fatal("disabled installation reaped volumes")
	}
}

func TestControllerBindVolumeYieldWritesNoScan(t *testing.T) {
	c, f := fixture(t)
	vr := &fakeBindVolumeRuntime{fakeRuntime: f, err: ErrBindVolumeYielded}
	c.Connect = func(context.Context) (Runtime, error) { return vr, nil }
	c.Tick(context.Background())
	var n int
	if err := c.DB.QueryRow(`SELECT count(*) FROM resource_cleanup_scans`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("interrupted backup admission wrote scan: count=%d error=%v", n, err)
	}
}
