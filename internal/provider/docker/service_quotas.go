package docker

import (
	"fmt"
	"reflect"

	"github.com/moby/moby/api/types/container"
)

const serviceQuotaPolicyVersion = "service-quotas-v1"

// These are host-enforced service bounds. Writable roots and named data volumes
// remain trusted legacy storage until an explicitly quota-backed catalog exists.
func applyServiceQuotas(h *container.HostConfig) {
	pids := int64(512)
	h.Resources.Memory = sidecarMemoryBytes
	h.Resources.MemorySwap = sidecarMemoryBytes // deny extra swap allocation
	h.Resources.NanoCPUs = sidecarNanoCPUs
	h.Resources.PidsLimit = &pids
	// Rotation bounds retained logs; nonblocking buffering may drop logs when
	// the daemon cannot keep up rather than stalling the service indefinitely.
	h.LogConfig = container.LogConfig{Type: "json-file", Config: map[string]string{"max-size": "10m", "max-file": "3", "mode": "non-blocking", "max-buffer-size": "1m"}}
	// Keep /tmp executable for legacy service image compatibility. This bounds
	// that tmpfs, not the writable image root or persistent data volumes.
	h.Tmpfs = map[string]string{"/tmp": "rw,nosuid,nodev,size=67108864,mode=1777"}
}
func checkServiceQuotas(h *container.HostConfig) error { return checkServiceQuotaProfile(h, false) }
func checkServiceQuotaProfile(h *container.HostConfig, hard bool) error {
	if h == nil {
		return fmt.Errorf("service HostConfig unavailable")
	}
	want := &container.HostConfig{}
	applyServiceQuotas(want)
	if hard {
		if h.RestartPolicy.Name != container.RestartPolicyDisabled {
			return fmt.Errorf("quota service autonomous restart bypass")
		}
		want.ReadonlyRootfs = true
		want.Tmpfs["/run"] = "rw,nosuid,nodev,noexec,size=16777216,mode=0755"
	}
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
