package restrictedworkflow

// The host advertises only profiles with an installed typed proof adapter.
// Absence preserves text-only compatibility; it never infers native support.
type profileExecutor interface {
	SupportsWorkflowProfile(string) bool
}

func (s *Service) supportsProfile(profile string) bool {
	if executor, ok := s.executor.(profileExecutor); ok {
		return executor.SupportsWorkflowProfile(profile)
	}
	_, typed := s.executor.(proofExecutor)
	return typed && profile == "responses_text"
}
