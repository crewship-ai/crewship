package api

import (
	"net/http/httptest"
	"testing"
)

func TestBoardHumanRouterDisabledByDefault(t *testing.T) {
	h, f, _ := boardHumanFixture(t)
	router, err := NewRouter(h.db, "board-router-qualification-secret", newTestLogger())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(router.Shutdown)
	request := httptest.NewRequest("GET", "/api/v1/pages/private-board/board?workspace_id="+f.wsID, nil)
	request.Header.Set("Authorization", "Bearer crewship_cli_board_"+f.userID)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != 404 {
		t.Fatalf("disabled Board route served %d: %s", response.Code, response.Body.String())
	}
}
