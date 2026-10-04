// Package stagedstart implements the trusted, boot-bound image execution gate.
// It carries lifecycle evidence only; it is not credential authority.
package stagedstart

const (
	Binary    = "/usr/local/bin/crewship-sidecar"
	Bootstrap = "/usr/local/bin/entrypoint.sh"
	Socket    = "@crewship-staged-v1"
	Label     = "crewship.staged-start"
	Version   = "1"
)

type Request struct {
	Operation string `json:"operation"`
	Nonce     string `json:"nonce,omitempty"`
	Challenge string `json:"challenge,omitempty"`
}
type Status struct {
	Nonce     string `json:"nonce"`
	Phase     string `json:"phase"`
	Challenge string `json:"challenge,omitempty"`
	Error     string `json:"error,omitempty"`
}

// State is guarded by the keeper's mutex. Consume is the only transition that
// permits image bootstrap; a lost completion never resets this state.
type State struct{ Nonce, Phase string }

func (s *State) Apply(r Request, uid uint32) Status {
	out := Status{Nonce: s.Nonce, Phase: s.Phase, Challenge: r.Challenge}
	if r.Operation == "status" && uid == 1002 {
		return out
	}
	if r.Nonce == "" || r.Nonce != s.Nonce {
		out.Error = "staged start: stale boot"
		return out
	}
	switch {
	case r.Operation == "seal" && uid == 1002 && s.Phase == "staging":
		s.Phase = "sealed"
	case r.Operation == "seal" && uid == 1002 && s.Phase == "sealed":
	case r.Operation == "reserve" && uid == 1002 && s.Phase == "sealed":
		s.Phase = "reserved"
	case r.Operation == "consume" && uid == 1001 && s.Phase == "reserved":
		s.Phase = "bootstrapping"
	case r.Operation == "exec" && s.Phase == "ready":
	default:
		out.Error = "staged start: operation denied"
	}
	out.Phase = s.Phase
	return out
}
