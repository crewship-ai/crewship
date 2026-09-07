package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/providerlogin"
)

func TestProviderLoginRefreshNotification_AdminAudience(t *testing.T) {
	t.Parallel()
	for _, creatorRole := range []string{"OWNER", "MEMBER"} {
		t.Run(creatorRole, func(t *testing.T) {
			r := newPLRig(t)
			id := r.seedCodexLogin(t, "private-provider-account", time.Hour)
			if _, err := r.db.Exec(`UPDATE workspace_members SET role = ? WHERE workspace_id = ? AND user_id = ?`, creatorRole, r.wsID, r.userID); err != nil {
				t.Fatal(err)
			}
			r.tokens.err = errors.New("token endpoint returned 503")
			for i := 0; i < providerlogin.MaxFailures; i++ {
				if _, err := r.rf.Refresh(context.Background(), id, true); err == nil {
					t.Fatal("expected refresh failure")
				}
			}
			// Evaluate the persisted notification as the same user after a
			// role change: a personal target would bypass current admin RBAC.
			for _, role := range []string{"OWNER", "ADMIN", "MANAGER", "MEMBER", "VIEWER"} {
				clause, args := inboxVisibilityClause(r.userID, role)
				args = append([]any{r.wsID, "provider-login-relogin:" + id}, args...)
				var count int
				if err := r.db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE workspace_id = ? AND source_id = ?`+clause, args...).Scan(&count); err != nil {
					t.Fatal(err)
				}
				want := 0
				if role == "OWNER" || role == "ADMIN" {
					want = 1
				}
				if count != want {
					t.Errorf("%s sees %d provider notifications, want %d", role, count, want)
				}
			}
		})
	}
}
