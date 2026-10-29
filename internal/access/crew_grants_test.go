package access

import (
	"errors"
	"testing"
)

func TestCrewGrantAgentInheritance(t *testing.T) {
	for _, operation := range []string{"view", "use"} {
		t.Run(operation, func(t *testing.T) {
			s := fixture(t)
			for _, query := range []string{
				`INSERT INTO crews(id,workspace_id,name,slug) VALUES('hidden','w','Hidden','hidden'),('foreign-crew','other','Foreign','foreign')`,
				`INSERT INTO agents(id,workspace_id,crew_id,name,slug,agent_role) VALUES('hidden-agent','w','hidden','Hidden canary','hidden','AGENT'),('foreign-agent','other','foreign-crew','Foreign canary','foreign','AGENT')`,
			} {
				if _, err := s.DB.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			policy(t, s, "h2")
			policy(t, s, "h1", Right{"crew", "crew", operation}, Right{"agent", "a", "discover"})
			for _, agent := range []string{"a", "b", "hidden-agent", "foreign-agent"} {
				for _, op := range []string{"discover", "chat", "run", "delegate"} {
					allowed := (agent == "a" || agent == "b") && (op == "discover" || (operation == "use" && (op == "chat" || op == "run")))
					if err := s.Check(t.Context(), "h1", "w", Right{"agent", agent, op}); (err == nil) != allowed {
						t.Errorf("%s/%s: %v", agent, op, err)
					}
					if err := s.Check(t.Context(), "h2", "w", Right{"agent", agent, op}); !errors.Is(err, ErrDenied) {
						t.Errorf("ungranted recipient %s/%s: %v", agent, op, err)
					}
				}
			}
			p, err := s.Policy(t.Context(), "owner", "h1", "w")
			if err != nil || len(p.Rights) != 2 || len(p.GrantDetails) != 2 {
				t.Fatalf("saved policy expanded or lost provenance: %+v %v", p, err)
			}
			for _, g := range p.GrantDetails {
				if !g.Live || g.IssuerID != "owner" {
					t.Fatalf("invalid live provenance: %+v", g)
				}
			}
			handle, _, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", nil)
			if operation == "view" {
				if !errors.Is(err, ErrDenied) {
					t.Fatalf("view admitted execution: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.Resolve(t.Context(), handle); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestCrewGrantRevisionFencesNeverReviveAttempts(t *testing.T) {
	for _, tc := range []struct{ name, change, restore string }{
		{"agent moves", `UPDATE agents SET crew_id='replacement' WHERE id='a'`, `UPDATE agents SET crew_id='crew' WHERE id='a'`},
		{"agent retires", `UPDATE agents SET deleted_at='now' WHERE id='a'`, `UPDATE agents SET deleted_at=NULL WHERE id='a'`},
		{"crew retires", `UPDATE crews SET deleted_at='now' WHERE id='crew'`, `UPDATE crews SET deleted_at=NULL WHERE id='crew'`},
		{"issuer loses role", `UPDATE workspace_members SET role='MEMBER' WHERE id='mo'`, `UPDATE workspace_members SET role='OWNER' WHERE id='mo'`},
		{"issuer suspended", `UPDATE users SET suspended_at='now' WHERE id='owner'`, `UPDATE users SET suspended_at=NULL WHERE id='owner'`},
		{"grant revoked", `DELETE FROM access_grants WHERE member_id='m1' AND resource_kind='crew'`, ``},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := fixture(t)
			if _, err := s.DB.Exec(`INSERT INTO crews(id,workspace_id,name,slug) VALUES('replacement','w','Replacement','replacement')`); err != nil {
				t.Fatal(err)
			}
			policy(t, s, "h1", Right{"crew", "crew", "use"})
			handle, _, err := s.Admit(t.Context(), "h1", "w", "a", "c1", "", nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.DB.Exec(tc.change); err != nil {
				t.Fatal(err)
			}
			if _, err = s.Resolve(t.Context(), handle); !errors.Is(err, ErrDenied) {
				t.Fatalf("old attempt survived change: %v", err)
			}
			if err = s.Check(t.Context(), "h1", "w", Right{"agent", "a", "run"}); !errors.Is(err, ErrDenied) {
				t.Fatalf("lost source still grants run: %v", err)
			}
			if tc.restore != "" {
				if _, err = s.DB.Exec(tc.restore); err != nil {
					t.Fatal(err)
				}
			} else {
				policy(t, s, "h1", Right{"crew", "crew", "use"})
			}
			if _, err = s.Resolve(t.Context(), handle); !errors.Is(err, ErrDenied) {
				t.Fatalf("restored source revived old attempt: %v", err)
			}
		})
	}
}

func TestCrewGrantRejectsForeignAndGuestManagement(t *testing.T) {
	s := fixture(t)
	if _, err := s.DB.Exec(`INSERT INTO crews(id,workspace_id,name,slug) VALUES('foreign-crew','other','Foreign','foreign')`); err != nil {
		t.Fatal(err)
	}
	for _, r := range []Right{{"crew", "foreign-crew", "view"}, {"crew", "crew", "manage"}, {"crew", "crew", "delegate"}} {
		m, err := s.Membership(t.Context(), "h1", "w")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.Replace(t.Context(), "owner", "h1", "w", "restricted", m, []Right{r}); !errors.Is(err, ErrDenied) {
			t.Fatalf("accepted %+v: %v", r, err)
		}
	}
}
