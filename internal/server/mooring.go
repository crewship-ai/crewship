package server

import (
	"errors"
	"time"

	goapi "github.com/crewship-ai/crewship/internal/api"
	"github.com/crewship-ai/crewship/internal/mooringassets"
)

// configureMooringHTTP selects only release-embedded, verified runtime bytes.
// Operator configuration selects enablement and lease policy, never a binary
// path, version tuple or participant-selected origin authority.
func (s *Server) configureMooringHTTP(opts []goapi.RouterOption) []goapi.RouterOption {
	if !s.cfg.Mooring.Enabled {
		return opts
	}
	lease := s.cfg.Mooring.DeliveryLease()
	if lease < time.Millisecond || lease > 5*time.Second {
		s.mooringStartupErr = errors.New("invalid Mooring delivery lease")
		return opts
	}
	prepared, err := mooringassets.Prepare("")
	if err != nil {
		s.mooringStartupErr = err
		return opts
	}
	// This is the fixed D1 application contract used by the private session
	// bootstrap. An incompatible release must fail before exposing routes.
	if prepared.Manifest.Versions != (mooringassets.Versions{Protocol: "0.4.0", Model: "0.2.0", Engine: "0.10.0"}) {
		_ = prepared.Close()
		s.mooringStartupErr = errors.New("unsupported embedded Mooring versions")
		return opts
	}
	s.mooringAssets = prepared
	return append(opts, goapi.WithMooringHTTP(goapi.MooringHTTPConfig{
		Enabled:      true,
		Binary:       prepared.Path,
		BinarySHA256: prepared.Manifest.BinarySHA256,
		PublicOrigin: s.cfg.PageStudioOrigin(),
		SDKPrefix:    prepared.Manifest.SDKPrefix,
		DeliveryTTL:  lease,
	}))
}

func (s *Server) closeMooringAssets() {
	if s.mooringAssets != nil {
		if err := s.mooringAssets.Close(); err != nil {
			s.logger.Error("Mooring runtime asset cleanup failed", "error", err)
		}
	}
}
