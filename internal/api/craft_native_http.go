package api

import (
	"crypto/sha256"
	"net/http"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// Dormant native-only human session/lease HTTP adapter. Router wiring remains
// explicit and disabled until native engine and rights lifecycle qualification.
type craftNativeSession struct {
	id                                    string
	tokenHash                             [32]byte
	source                                auth.OriginalCredential
	scope                                 mooring.EditLeaseScope
	application, workspaceEpoch, memberID string
	memberRevision                        int64
	boot                                  string
	deadline                              time.Duration
}
type craftNativeRequest struct {
	id, target string
	generation int64
}
type craftNativeHTTP struct {
	pages          *PageHandler
	enabled        bool
	clock          mooring.OwnerClock
	leases         *mooring.EditLeaseStore
	mu             sync.Mutex
	sessionsByHash map[[sha256.Size]byte]*craftNativeSession
	sessionsByID   map[string]*craftNativeSession
	requests       map[string]craftNativeRequest
	live           map[string]bool
}

func newCraftNativeHTTP(pages *PageHandler, enabled bool, clock mooring.OwnerClock) *craftNativeHTTP {
	m := &craftNativeHTTP{pages: pages, enabled: enabled, clock: clock, sessionsByHash: make(map[[sha256.Size]byte]*craftNativeSession), sessionsByID: make(map[string]*craftNativeSession), requests: make(map[string]craftNativeRequest), live: make(map[string]bool)}
	if pages != nil {
		m.leases = mooring.NewEditLeaseStore(pages.db, mooring.EditLeaseConfig{Enabled: enabled, Clock: clock, TTL: 15 * time.Second, Authority: m.leaseAuthority})
	}
	return m
}
func (m *craftNativeHTTP) sessions(w http.ResponseWriter, r *http.Request) {
	m.issue(w, r)
}

// Explicit fresh-authentication renewal never adopts a new identity or epoch.
func (m *craftNativeHTTP) reauthorize(w http.ResponseWriter, r *http.Request) {
	renewed, err := m.renewAdmission(r)
	if err != nil {
		craftNativeHTTPError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]bool{"renewed": renewed})
}
