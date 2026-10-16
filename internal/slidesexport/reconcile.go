package slidesexport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"time"
)

type ownedContainer struct {
	ID    string `json:"id"`
	Owner string `json:"owner"`
	Token string `json:"token"`
}

const inspectTemplate = `{"id":{{json .Id}},"owner":{{json (index .Config.Labels "crewship.slides.export.owner")}},"token":{{json (index .Config.Labels "crewship.slides.export.token")}}}`

func inspectOwned(ctx context.Context, v cleanupIntent) (*ownedContainer, error) {
	deadline, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, logs := &capture{limit: 64 << 10, cancel: cancel}, &capture{limit: 64 << 10, cancel: cancel}
	target := v.Name
	if v.ContainerID != "" {
		target = v.ContainerID
	}
	cmd := exec.CommandContext(deadline, "docker", "inspect", "--type=container", "--format", inspectTemplate, target)
	cmd.WaitDelay = time.Second
	cmd.Stdout = out
	cmd.Stderr = logs
	err := cmd.Run()
	if out.overflow || logs.overflow {
		return nil, ErrCleanup
	}
	if err != nil {
		if deadline.Err() != nil {
			return nil, ErrCleanup
		}
		if strings.Contains(logs.buffer.String(), "No such object") || strings.Contains(logs.buffer.String(), "No such container") {
			return nil, nil
		}
		return nil, ErrCleanup
	}
	raw := bytes.TrimSpace(out.buffer.Bytes())
	if strictJSON(raw) != nil {
		return nil, ErrCleanup
	}
	if _, err = exact(raw, "id", "owner", "token"); err != nil {
		return nil, ErrCleanup
	}
	var c ownedContainer
	if json.Unmarshal(raw, &c) != nil || !digestPattern.MatchString(c.ID) || c.Owner != v.OwnerID || c.Token != v.Token || (v.ContainerID != "" && v.ContainerID != c.ID) {
		return nil, ErrCleanup
	}
	return &c, nil
}
func removeOwned(ctx context.Context, c *ownedContainer) error {
	deadline, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, logs := &capture{limit: 64 << 10, cancel: cancel}, &capture{limit: 64 << 10, cancel: cancel}
	cmd := exec.CommandContext(deadline, "docker", "rm", "--force", c.ID)
	cmd.WaitDelay = time.Second
	cmd.Stdout = out
	cmd.Stderr = logs
	err := cmd.Run()
	if out.overflow || logs.overflow {
		return ErrCleanup
	}
	if err != nil {
		if deadline.Err() != nil {
			return ErrCleanup
		}
		if strings.Contains(logs.buffer.String(), "No such container") {
			return nil
		}
		return ErrCleanup
	}
	return nil
}

// Reconciliation proves only removal observed inside its finite deadline. For
// ambiguous create completion the caller MUST retain uncertainty after this.
func (r *Runner) reconcileOwned(ctx context.Context, v cleanupIntent, ambiguous bool) error {
	for {
		c, err := inspectOwned(ctx, v)
		if err != nil {
			return err
		}
		if c != nil {
			err = removeOwned(ctx, c)
			if err == nil && !ambiguous {
				return nil
			}
			// AutoRemove can race an explicit removal. Retry only after another
			// exact ownership inspection, inside the same finite cleanup budget.
		} else if !ambiguous {
			return nil
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(ErrCleanup, ctx.Err())
		case <-timer.C:
		}
	}
}
