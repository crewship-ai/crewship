package restrictedworkflow

// The host advertises only profiles with an installed typed proof adapter.
// Absence preserves text-only compatibility; it never infers native support.
type profileExecutor interface {
	SupportsWorkflowProfile(string) bool
}

func (s *Service) supportsProfile(profile string) bool {
	// Navigation checks supported declaration types, not installed executors.
	// Graph/provider authority is still validated, and no execution uses this view.
	if s.authorityOnly {
		return profile == "responses_text" || profile == "native_api_key"
	}
	if executor, ok := s.executor.(profileExecutor); ok {
		return executor.SupportsWorkflowProfile(profile)
	}
	_, typed := s.executor.(proofExecutor)
	return typed && profile == "responses_text"
}
