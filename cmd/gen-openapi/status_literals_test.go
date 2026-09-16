package main

import (
	"reflect"
	"testing"
)

func TestAbsorbHandlerBody_StatusLiterals(t *testing.T) {
	for _, tc := range []struct{ body, status string }{
		{`writeProblem(w, r, 400, "name must be 2-100 characters")`, "400"},
		{`replyError(w, 409, "conflict")`, "409"},
		{`internalError(w, r, err)`, "500"},
		{`replyInternalError(w, err)`, "500"},
		{`writeProblem(w, r, http.StatusConflict, "conflict")`, "409"},
		{`writeJSON(w, http.StatusBadRequest, map[string]string{"error":"2-100 characters"})`, "400"},
		{`writeJSON(w, 202, map[string]string{"message":"wait 500 seconds"})`, "202"},
		{`w.WriteHeader(204)`, "204"},
	} {
		t.Run(tc.body, func(t *testing.T) {
			info := handlerInfo{query: map[string]queryParam{}, statuses: map[string]bool{}}
			absorbHandlerBody(&info, "", tc.body)
			if want := map[string]bool{tc.status: true}; !reflect.DeepEqual(info.statuses, want) {
				t.Fatalf("statuses = %v, want %v", info.statuses, want)
			}
		})
	}
}
