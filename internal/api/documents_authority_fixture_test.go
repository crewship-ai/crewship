package api

import "testing"

// Adapt the existing private-listener regressions to the current Pages fixture;
// publishing artifacts and delegated agent requests are separate slices.
func documentAuthorityFixture(t *testing.T) (*PageHandler, string, string, string) {
	t.Helper()
	rig := pagesTransferSetup(t)
	const page = "document-authority-page"
	rig.seedPage(t, page, page, "Document", rig.adminID, "")
	return NewPageHandler(rig.db, nil, newTestLogger()), rig.wsID, rig.adminID, page
}
