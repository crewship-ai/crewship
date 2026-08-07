package api

// The configured-vs-effective gap on a crew's container (#1681).
//
// container_memory_mb and container_cpus are applied at ContainerCreate and
// nowhere else, so editing them changes the row, returns 200, and moves
// `crew get` — while the running container keeps the limits it was born with.
// #1681's other half makes a STOPPED container converge on the next wake
// (internal/provider/docker/crew_resource_drift.go); a running one is
// deliberately never killed for a resize, so somewhere has to say that the
// crew is not yet running under what it is configured for.
//
// This is that somewhere, and it is the only place the comparison can be made
// honestly: crewshipd reports what the container carries (read off the
// inspect, never recomputed from a spec), and this package holds the crews
// row. Neither side alone knows both numbers, and nothing reconstructs either
// one — which is what keeps this free of the disagreement risk that kept the
// per-crew case out of #1642's contract digest in the first place.

// crewContainerDrift annotates a container-status payload with the crew's
// CONFIGURED limits and the fields where they disagree with the EFFECTIVE ones
// crewshipd reported.
//
// The payload is the decoded IPC response and is mutated in place. It gains:
//
//	configured_memory_mb / configured_cpus — always, so a reader never has to
//	  guess what the comparison was made against
//	config_drift — one entry per disagreeing field, omitted entirely when
//	  there is nothing to report
//
// Absence is not zero. crewshipd omits effective_memory_mb / effective_cpus
// when its provider has no opinion — the Apple provider tracks neither, and
// the docker provider says nothing about a container that declares no limit —
// and comparing a missing number against a configured 8192 would manufacture
// drift out of silence. So a field is compared only when the status actually
// carried it.
func crewContainerDrift(payload map[string]any, configuredMemoryMB int, configuredCPUs float64) {
	if payload == nil {
		return
	}
	payload["configured_memory_mb"] = configuredMemoryMB
	payload["configured_cpus"] = configuredCPUs

	type driftEntry struct {
		Field      string  `json:"field"`
		Configured float64 `json:"configured"`
		Effective  float64 `json:"effective"`
	}
	var drift []driftEntry

	if effective, ok := numericField(payload, "effective_memory_mb"); ok && configuredMemoryMB > 0 && effective != float64(configuredMemoryMB) {
		drift = append(drift, driftEntry{"container_memory_mb", float64(configuredMemoryMB), effective})
	}
	if effective, ok := numericField(payload, "effective_cpus"); ok && configuredCPUs > 0 && effective != configuredCPUs {
		drift = append(drift, driftEntry{"container_cpus", configuredCPUs, effective})
	}

	if len(drift) > 0 {
		payload["config_drift"] = drift
	}
}

// numericField reads a JSON number out of a decoded payload. Reports false for
// an absent field, a null, or a non-number — all of which mean "this status
// said nothing about that limit", never "zero".
func numericField(payload map[string]any, key string) (float64, bool) {
	raw, ok := payload[key]
	if !ok || raw == nil {
		return 0, false
	}
	switch v := raw.(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	default:
		return 0, false
	}
}
