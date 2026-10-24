package orchestrator

import "errors"

// ErrPrivateRunUnqualified deliberately carries no child-controlled message
// and no wrapped diagnostic. It is not proof of a terminal permission denial.
var ErrPrivateRunUnqualified = errors.New("private runtime outcome requires qualified inspection")

func privateRunError(req AgentRunRequest, err error) error {
	if req.strictContentPrivacy && err != nil {
		return ErrPrivateRunUnqualified
	}
	return err
}

// WithStrictContentPrivacy selects host-side output suppression for a trusted
// caller. It is not a complete private execution environment: native CLI disk
// history, tools, container files and their readouts need separate qualification.
// The unexported bit cannot be populated or cleared by JSON request fields.
func (r AgentRunRequest) WithStrictContentPrivacy() AgentRunRequest {
	r.strictContentPrivacy = true
	r.SuppressSessionStream = true
	r.SkipConvHistory = true
	r.MemoryEnabled = false
	return r
}
