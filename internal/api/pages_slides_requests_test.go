package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/slides"
	"github.com/crewship-ai/crewship/internal/ws"
)

func selectedSlidesHTTPFixture(t *testing.T) (*instanceFixture, string, slides.SelectedRequestInput) {
	t.Helper()
	f := newInstanceFixture(t)
	t.Cleanup(f.r.Shutdown)
	const page = "selected-page"
	wmSeedPage(t, f.db, "ws-old", page, page, "Selected Slides", "boss", "")
	base := "/api/v1/pages/" + page + "/slides"
	deck := slideTestDeck(t, page)
	wantCode(t, f.slideDo(f.boss, "POST", base+"/initialize", slideJSON(t, slides.InitializeRequest{PageID: page, SchemaVersion: 1, MutationID: "init", Deck: deck})), 200, "initialize")
	mustExec(t, f.db, `INSERT INTO crews(id,workspace_id,name,slug) VALUES('selected-crew','ws-old','Selected','selected')`)
	seedAgentRow(t, f.db, "selected-agent", "ws-old", "selected-crew", "Selected agent", "selected", "AGENT")
	mustExec(t, f.db, `INSERT INTO chats(id,workspace_id,agent_id,created_by,visibility) VALUES('selected-chat','ws-old','selected-agent','boss','private')`)
	mustExec(t, f.db, `INSERT INTO page_grants(page_id,subject_type,subject_id,level,granted_by_user_id) VALUES('selected-page','agent','selected-agent','write','boss')`)
	f.r.pages.artifactDispatch = func(context.Context, string, string, string, ws.ChatMessageOption) error { return nil }
	for _, slide := range deck.Slides {
		for _, element := range slide.Elements {
			if element.Type == "text" && element.Text != nil {
				hash, _ := slides.ElementHash(element)
				selection := slides.TextSelection{SelectionSchemaVersion: 1, Kind: "text_range", SlideID: slide.ID, ElementID: element.ID, ElementHash: hash, From: 0, To: len(utf16.Encode([]rune(element.Text.Value))), Quote: element.Text.Value}
				return f, base, slides.SelectedRequestInput{PageID: page, TypeID: "slides", SchemaVersion: 1, BaseRev: 1, SubmissionID: "selected-submission", ChatID: "selected-chat", Instruction: "Uprav český vybraný text", Selection: selection}
			}
		}
	}
	t.Fatal("fixture has no text")
	return nil, "", slides.SelectedRequestInput{}
}

func TestSlidesSelectedActualRouterAdmissionReadCancelAndLiveAgentGrant(t *testing.T) {
	f, base, input := selectedSlidesHTTPFixture(t)
	body := slideJSON(t, input)
	response := f.slideDo(f.boss, "POST", base+"/requests", body)
	wantCode(t, response, 202, "admit selected request")
	var admitted SlidesSelectedAdmissionResponse
	if err := json.Unmarshal(response.Body.Bytes(), &admitted); err != nil || admitted.ID == "" {
		t.Fatal(response.Body.String(), err)
	}
	retry := f.slideDo(f.boss, "POST", base+"/requests", body)
	wantCode(t, retry, 202, "exact admission retry")
	var duplicate SlidesSelectedAdmissionResponse
	if err := json.Unmarshal(retry.Body.Bytes(), &duplicate); err != nil || !duplicate.Duplicate || duplicate.ID != admitted.ID {
		t.Fatal(retry.Body.String(), err)
	}
	read := f.slideDo(f.boss, "GET", base+"/requests/"+admitted.ID, "")
	wantCode(t, read, 200, "read immutable selection")
	if strings.Contains(read.Body.String(), "credential_") || strings.Contains(read.Body.String(), "audience_json") || strings.Contains(read.Body.String(), f.boss) {
		t.Fatal("public response leaked authority", read.Body.String())
	}
	var detail SlidesSelectedRequestResponse
	if err := json.Unmarshal(read.Body.Bytes(), &detail); err != nil || detail.Request.Selection != input.Selection || len(detail.History) != 1 {
		t.Fatal(read.Body.String(), err)
	}
	wantCode(t, f.slideDo(f.boss, "GET", base+"/requests?limit=1", ""), 200, "bounded list")
	mustExec(t, f.db, `DELETE FROM page_grants WHERE page_id='selected-page' AND subject_type='agent'`)
	wantCode(t, f.slideDo(f.boss, "POST", base+"/requests", body), 403, "revoked original agent grant denies admission replay")
	cancel := slides.CancelSelectedRequestInput{PageID: input.PageID, SchemaVersion: 1, MutationID: "cancel", IfVersion: 1}
	wantCode(t, f.slideDo(f.boss, "POST", base+"/requests/"+admitted.ID+"/cancel", slideJSON(t, cancel)), 200, "human can stop revoked agent")
	wantCode(t, f.slideDo(f.boss, "POST", base+"/requests/"+admitted.ID+"/cancel", slideJSON(t, cancel)), 200, "exact cancellation replay")
	var head int
	if err := f.db.QueryRow(`SELECT head_rev FROM slides_heads WHERE page_id=?`, input.PageID).Scan(&head); err != nil || head != 1 {
		t.Fatal("request metadata changed content", head, err)
	}
}

func TestSlidesSelectedActualRouterExactCredentialRevocationDuringBodyAndReplay(t *testing.T) {
	for _, credential := range []string{"cli", "session"} {
		for _, replay := range []bool{false, true} {
			name := credential + "/new"
			if replay {
				name = credential + "/replay"
			}
			t.Run(name, func(t *testing.T) {
				f, base, input := selectedSlidesHTTPFixture(t)
				token := f.boss
				revoke := func() {
					mustExec(t, f.db, `UPDATE cli_tokens SET revoked_at='2026-01-01T00:00:00Z' WHERE token_hash=?`, sha256Hex(token))
				}
				if credential == "session" {
					now := time.Now().UTC()
					mustExec(t, f.db, `INSERT INTO user_sessions(id,user_id,created_at,expires_at,last_used_at) VALUES('selected-session','boss',?,?,?)`, now.Format(time.RFC3339Nano), now.Add(time.Hour).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
					validator, err := auth.NewJWTValidator("this-is-a-32-char-test-secret-pad")
					if err != nil {
						t.Fatal(err)
					}
					token, err = validator.IssueAccessToken("boss", "selected-session", "Boss", "boss@ex.com")
					if err != nil {
						t.Fatal(err)
					}
					revoke = func() {
						mustExec(t, f.db, `UPDATE user_sessions SET revoked_at='2026-01-01T00:00:00Z' WHERE id='selected-session'`)
					}
				}
				body := slideJSON(t, input)
				if replay {
					wantCode(t, f.slideDo(token, "POST", base+"/requests", body), 202, "first acknowledged admission")
				}
				r := httptest.NewRequest("POST", base+"/requests?workspace_id=ws-old", strings.NewReader(body))
				r.Header.Set("Authorization", "Bearer "+token)
				r.Body = &artifactRevokingBody{reader: strings.NewReader(body), revoke: revoke}
				response := httptest.NewRecorder()
				f.r.ServeHTTP(response, r)
				wantCode(t, response, 403, "exact credential revoked during body")
				var count int
				if err := f.db.QueryRow(`SELECT COUNT(*) FROM slides_request_events`).Scan(&count); err != nil {
					t.Fatal(err)
				}
				want := 0
				if replay {
					want = 1
				}
				if count != want {
					t.Fatal("revoked admission changed history", count, want)
				}
				wantCode(t, f.slideDo(token, "POST", base+"/requests", body), 401, "revoked retry")
			})
		}
	}
}
