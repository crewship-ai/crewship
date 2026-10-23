package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/editororigin"
)

// The same-host workbook editor refuses a frame that carries Crewship's own
// cookies (it lost the credentialless attribute) and ignores cookies set by a
// proxy in front. Every cookie name this handler issues on the Studio origin
// must therefore be one the editor recognises.
func TestEditorRecognisesEveryAuthCookie(t *testing.T) {
	plain := httptest.NewRequest("GET", "http://studio.example.test/", nil)
	secure := httptest.NewRequest("GET", "https://studio.example.test/", nil)
	secure.Header.Set("X-Forwarded-Proto", "https")
	h := &NextAuthHandler{}
	for _, req := range []*http.Request{plain, secure} {
		for _, name := range []string{sessionCookieName(req), refreshCookieName(req), h.csrfCookieName(req)} {
			if !editororigin.IsCrewshipCookie(name) {
				t.Errorf("editor does not recognise Crewship cookie %q", name)
			}
		}
	}
}
