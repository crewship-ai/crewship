package server

import (
	"context"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// legacyResourceTTL bounds how often the legacy-resource docker scan runs.
// /healthz is hit frequently (load balancers, doctor, dashboards) and the
// underlying ContainerList+VolumeList is not free, so a definitive result is
// memoized for this window.
const legacyResourceTTL = 60 * time.Second

// legacyResourceCache memoizes the rendered legacy-resource status. Zero value
// is ready to use.
type legacyResourceCache struct {
	mu     sync.Mutex
	at     time.Time
	status string
	valid  bool
}

// legacyResourceStatus reports whether the daemon carries orphaned pre-C1
// slug-only crew resources (left over from before the C1 naming change, which
// survive nuke+reseed and block agent container start):
//
//	"present" — at least one legacy resource exists; operator should prune
//	"clean"   — no legacy resources for any current crew slug
//	""        — unknown: non-docker provider, no DB, or a transient scan error
//
// Cached for legacyResourceTTL. Transient "" results are deliberately NOT
// cached so a recovered daemon is re-checked on the next hit rather than masked
// for a full TTL. `crewship doctor` reads this via the /healthz response.
func (s *Server) legacyResourceStatus(ctx context.Context) string {
	detector, ok := s.container.(provider.LegacyResourceDetector)
	if !ok {
		return ""
	}
	s.legacyCache.mu.Lock()
	defer s.legacyCache.mu.Unlock()
	if s.legacyCache.valid && time.Since(s.legacyCache.at) < legacyResourceTTL {
		return s.legacyCache.status
	}
	status := s.computeLegacyResourceStatus(ctx, detector)
	if status != "" {
		s.legacyCache.status = status
		s.legacyCache.at = time.Now()
		s.legacyCache.valid = true
	}
	return status
}

// computeLegacyResourceStatus does the uncached work: enumerate live crew slugs
// and ask the docker provider whether any legacy resource exists for them.
func (s *Server) computeLegacyResourceStatus(ctx context.Context, detector provider.LegacyResourceDetector) string {
	if s.db == nil {
		return ""
	}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT slug FROM crews WHERE deleted_at IS NULL`)
	if err != nil {
		s.logger.Warn("legacy resource check: list crew slugs", "error", err)
		return ""
	}
	defer rows.Close()
	var slugs []string
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			s.logger.Warn("legacy resource check: scan slug", "error", err)
			return ""
		}
		slugs = append(slugs, slug)
	}
	if err := rows.Err(); err != nil {
		s.logger.Warn("legacy resource check: rows", "error", err)
		return ""
	}
	if len(slugs) == 0 {
		return "clean"
	}
	present, err := detector.HasLegacyCrewResources(ctx, slugs)
	if err != nil {
		s.logger.Warn("legacy resource check: detect", "error", err)
		return ""
	}
	if present {
		return "present"
	}
	return "clean"
}
