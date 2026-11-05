package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"

	"arkom/bia/internal/management"
)

func TestGoogleSellerPageIsPublicButStaffAPIsStayAuthenticated(t *testing.T) {
	panel := (&management.Server{}).Handler()
	handler := (&Server{Management: panel}).Handler()
	for _, path := range []string{"/conectar-agenda/", "/conectar-agenda/connect.js", "/conectar-agenda/connect.css"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" || !strings.Contains(w.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
			t.Fatal("seller entry point not available with safe headers", path, w.Code)
		}
		if path == "/conectar-agenda/" && (!strings.Contains(w.Body.String(), `method="post" action="/v1/management/auth/login"`) || !strings.Contains(w.Body.String(), `method="post" action="/v1/management/users"`)) {
			t.Fatal("a script-loading failure could submit credentials in the URL")
		}
	}
	for _, path := range []string{"/v1/management/integrations/google/status", "/v1/management/integrations/google/calendars", "/v1/management/users"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 401 {
			t.Fatal("public page exposed authenticated staff resource", path, w.Code)
		}
	}
	for _, path := range []string{"/conectar-agenda/secret.env", "/conectar-agenda/auth.go", "/conectar-agenda/google_connect/index.html"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 404 {
			t.Fatal("unexpected asset is public", path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	(&Server{}).Handler().ServeHTTP(w, httptest.NewRequest("GET", "/conectar-agenda/", nil))
	if w.Code != 404 {
		t.Fatal("seller page exposed with management disabled")
	}
}
