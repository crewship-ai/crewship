package restrictedworkflow

// Execution advertises only profiles with an installed typed proof adapter.
// Legacy typed executors imply text-only support. Read-only navigation below
// separately recognizes known declarations when no runtime is installed.
type profileExecutor interface {
	SupportsWorkflowProfile(string) bool
}

func (s *Service) supportsProfile(profile string) bool {
	// Navigation checks supported declaration types, not installed executors.
	// Graph/provider authority is still validated, and no execution uses this view.
	if s.authorityOnly {
		if s.declaredProfiles != nil {
			return s.declaredProfiles(profile)
		}
		return profile == "responses_text" || profile == "native_api_key"
	}
	if executor, ok := s.executor.(profileExecutor); ok {
		return executor.SupportsWorkflowProfile(profile)
	}
	_, typed := s.executor.(proofExecutor)
	return typed && profile == "responses_text"
}

// DeclaredProfileSupport is a static adapter declaration, not a runtime health
// check. Without an installed service, navigation uses known declaration types.
func (s *Service) DeclaredProfileSupport() func(string) bool {
	if s == nil {
		return nil
	}
	if declared, ok := s.executor.(interface{ DeclaresWorkflowProfile(string) bool }); ok {
		return declared.DeclaresWorkflowProfile
	}
	// Older typed text adapters have never implied native support. Avoid calling
	// their readiness-dependent SupportsWorkflowProfile from navigation.
	_, typed := s.executor.(proofExecutor)
	return func(profile string) bool { return typed && profile == "responses_text" }
}
