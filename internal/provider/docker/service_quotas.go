package docker

import (
	"fmt"
	"reflect"

	"github.com/moby/moby/api/types/container"
)

const serviceQuotaPolicyVersion = "service-quotas-v1"

// applyServiceQuotas is the host-enforced profile of a service that opted in
// with quota_enforced. Services that did not opt in keep the pre-quota
// profile (memory/CPU/PID caps only) and their operator's log driver.
func applyServiceQuotas(h *container.HostConfig) {
	pids := int64(512)
	h.Resources.Memory = sidecarMemoryBytes
	h.Resources.MemorySwap = sidecarMemoryBytes // deny extra swap allocation
	h.Resources.NanoCPUs = sidecarNanoCPUs
	h.Resources.PidsLimit = &pids
	// Rotation bounds retained logs; nonblocking buffering may drop logs when
	// the daemon cannot keep up rather than stalling the service indefinitely.
	h.LogConfig = container.LogConfig{Type: "json-file", Config: map[string]string{"max-size": "10m", "max-file": "3", "mode": "non-blocking", "max-buffer-size": "1m"}}
	// The image root is read-only; writable scratch space is bounded tmpfs
	// (charged to the memory limit). /tmp stays executable for image
	// compatibility.
	h.ReadonlyRootfs = true
	h.Tmpfs = map[string]string{
		"/tmp": "rw,nosuid,nodev,size=67108864,mode=1777",
		"/run": "rw,nosuid,nodev,noexec,size=16777216,mode=0755",
	}
}

// checkServiceQuotaProfile audits a quota-enforced container's actual
// HostConfig against applyServiceQuotas.
func checkServiceQuotaProfile(h *container.HostConfig) error {
	if h == nil {
		return fmt.Errorf("service HostConfig unavailable")
	}
	want := &container.HostConfig{}
	applyServiceQuotas(want)
	if h.ReadonlyRootfs != want.ReadonlyRootfs {
		return fmt.Errorf("service root write policy drift")
	}
	if h.Memory != want.Memory || h.MemorySwap != want.MemorySwap || h.NanoCPUs != want.NanoCPUs || h.PidsLimit == nil || *h.PidsLimit != *want.PidsLimit {
		return fmt.Errorf("service cgroup quota drift")
	}
	if h.LogConfig.Type != want.LogConfig.Type || !reflect.DeepEqual(h.LogConfig.Config, want.LogConfig.Config) {
		return fmt.Errorf("service log quota drift")
	}
	if !reflect.DeepEqual(h.Tmpfs, want.Tmpfs) {
		return fmt.Errorf("service temporary storage quota drift")
	}
	return nil
}

// Managed service desired state belongs to the leased controller. Docker must
// not independently resurrect a stopped service before controller startup.
func checkServiceRestartPolicy(h *container.HostConfig, managed bool) error {
	if h == nil {
		return fmt.Errorf("service HostConfig unavailable")
	}
	if managed && (h.RestartPolicy.Name != container.RestartPolicyDisabled || h.RestartPolicy.MaximumRetryCount != 0) {
		return fmt.Errorf("managed service automatic restart drift")
	}
	return nil
}
