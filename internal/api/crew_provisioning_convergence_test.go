package api

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/moby/moby/client"
)

type changingBuildClient struct {
	covCommitClient
	onList func()
}

func (c *changingBuildClient) ImageList(ctx context.Context, opts client.ImageListOptions) (client.ImageListResult, error) {
	c.onList()
	return c.covCommitClient.ImageList(ctx, opts)
}

func TestProvisioningConvergesAfterDefinitionChangesWithoutFailingPendingMessage(t *testing.T) {
	const oldConfig = `{"image":"ubuntu:22.04"}`
	const newConfig = `{"image":"ubuntu:24.04"}`
	h, wsID, crewID, _ := resumeTestRig(t, oldConfig)
	resumer := newFakeChatResumer()
	h.chatResumer = resumer
	job := covJob(crewID)
	h.jobs[crewID] = job
	h.rateLimiter.running[wsID] = 1
	if _, err := h.db.Exec(`UPDATE crews SET cached_image='previous' WHERE id=?`, crewID); err != nil {
		t.Fatal(err)
	}
	h.AttachPendingMessage(crewID, chatbridge.PendingChatMessage{UserID: "user-1", ChatID: "chat-1", Content: "keep waiting"})
	attempts := 0
	fake := &changingBuildClient{}
	fake.onList = func() {
		attempts++
		if attempts == 1 {
			if _, err := h.db.Exec(`UPDATE crews SET devcontainer_config=? WHERE id=?`, newConfig, crewID); err != nil {
				t.Fatal(err)
			}
		} else {
			if resumer.callCount() != 0 || len(job.Pending) != 1 {
				t.Fatal("superseded build completed or failed the pending message")
			}
			var selected string
			if err := h.db.QueryRow(`SELECT cached_image FROM crews WHERE id=?`, crewID).Scan(&selected); err != nil || selected != "previous" {
				t.Fatalf("stale build changed selection: %q %v", selected, err)
			}
		}
	}
	h.provisioner = devcontainer.NewProvisioner(fake, nil, nil, newTestLogger())
	h.runProvisioning(crewID, wsID, oldConfig, "", "", job)
	if job.Status != "completed" || attempts != 2 {
		t.Fatalf("status=%s error=%s attempts=%d", job.Status, job.Error, attempts)
	}
	h.prepareLatestCrew(crewID, wsID, oldConfig, "", "", job)
	if h.jobs[crewID] != job {
		t.Fatal("successful convergence unnecessarily enqueued another build")
	}
	if h.rateLimiter.running[wsID] != 0 {
		t.Fatal("build slot leaked")
	}
	select {
	case call := <-resumer.called:
		if call.content != "keep waiting" {
			t.Fatalf("wrong pending message: %+v", call)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("pending message did not resume after current build")
	}
	if resumer.callCount() != 1 {
		t.Fatal("pending message resumed more than once")
	}
}

func TestProvisioningContinuousEditsHaveBoundedRetries(t *testing.T) {
	const initial = `{"image":"ubuntu:22.04"}`
	h, wsID, crewID, _ := resumeTestRig(t, initial)
	job := covJob(crewID)
	h.jobs[crewID] = job
	h.rateLimiter.running[wsID] = 1
	attempts := 0
	fake := &changingBuildClient{}
	fake.onList = func() {
		attempts++
		next := fmt.Sprintf(`{"image":"ubuntu:edit-%d"}`, attempts)
		if _, err := h.db.Exec(`UPDATE crews SET devcontainer_config=? WHERE id=?`, next, crewID); err != nil {
			t.Fatal(err)
		}
	}
	h.provisioner = devcontainer.NewProvisioner(fake, nil, nil, newTestLogger())
	h.runProvisioning(crewID, wsID, initial, "", "", job)
	if job.Status != "failed" || !strings.Contains(job.Error, "kept changing") || attempts != 8 {
		t.Fatalf("unbounded or wrong termination: attempts=%d status=%s error=%s", attempts, job.Status, job.Error)
	}
	if h.rateLimiter.running[wsID] != 0 {
		t.Fatal("build slot leaked")
	}
}
