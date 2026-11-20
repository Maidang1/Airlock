package workbench

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// A MicroVM profile must answer the routes that only ever reached the Lima
// guest helpers with one clear refusal instead of misleading maintenance
// errors, while the host-side panel stays readable.
func TestMicroVMUnsupportedRoutes(t *testing.T) {
	s := testServer(t)
	s.microGuest = &microVMRuntime{}
	s.claudeAccounts.probe = func(context.Context, string) (claudeGuestSnapshot, error) {
		t.Error("managed-account probe must not reach the MicroVM guest")
		return claudeGuestSnapshot{}, errors.New("probe must not run")
	}
	s.claudeAccounts.usageProbe = func(context.Context, string, string) (claudeGuestSnapshot, error) {
		t.Error("usage probe must not reach the MicroVM guest")
		return claudeGuestSnapshot{}, errors.New("probe must not run")
	}
	for _, tc := range []struct {
		method, path string
	}{
		{"POST", "/api/claude-accounts"},
		{"GET", "/api/claude-desktop"},
		{"POST", "/api/claude-desktop/install"},
		{"GET", "/api/claude-desktop/vnc-credentials"},
		{"POST", "/api/vault/browser"},
		{"POST", "/api/browser/extensions/upload"},
		{"GET", "/api/browser/files/downloads/01234567-89ab-cdef-0123-456789abcdef"},
		{"POST", "/api/browser/files/uploads/01234567-89ab-cdef-0123-456789abcdef"},
	} {
		w := request(s, tc.method, tc.path, s.origin, "{}", true)
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "此功能尚未接入 MicroVM 开发环境") {
			t.Fatalf("%s %s under MicroVM: %d %s", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
	if w := request(s, "POST", "/api/claude-accounts", s.origin, "{}", false); w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated request reached the MicroVM guard: %d", w.Code)
	}
	// Host-side panels keep working so the UI can explain itself.
	if w := request(s, "GET", "/api/browser/files/saved", "", "", true); w.Code != http.StatusOK {
		t.Fatalf("saved downloads are host-side: %d %s", w.Code, w.Body.String())
	}
	w := request(s, "GET", "/api/claude-accounts", "", "", true)
	if w.Code != http.StatusOK {
		t.Fatalf("account panel under MicroVM: %d %s", w.Code, w.Body.String())
	}
	var panel claudePanel
	if err := json.Unmarshal(w.Body.Bytes(), &panel); err != nil {
		t.Fatal(err)
	}
	if panel.DetectionStatus != "unsupported" || !strings.Contains(panel.DetectionError, "MicroVM") {
		t.Fatalf("account panel under MicroVM does not state the limitation: %+v", panel)
	}
}

// The refusal must stay scoped to MicroVM: other environments keep the
// original handlers, including their own error messages.
func TestMicroVMGuardDoesNotAffectOtherEnvironments(t *testing.T) {
	s := testServer(t)
	w := request(s, "POST", "/api/claude-accounts", s.origin, `{"action":"bogus"}`, true)
	if w.Code != http.StatusConflict || strings.Contains(w.Body.String(), "MicroVM") {
		t.Fatalf("non-MicroVM account mutation: %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "未知 Claude 账号操作") {
		t.Fatalf("non-MicroVM account mutation lost its handler: %d %s", w.Code, w.Body.String())
	}
}

func TestMicroVMUnsupportedRouteTable(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		want         bool
	}{
		{"POST", "/api/claude-accounts", true},
		{"GET", "/api/claude-accounts", false},
		{"GET", "/api/claude-desktop", true},
		{"POST", "/api/claude-desktop/resize", true},
		{"GET", "/api/claude-desktop/vnc", true},
		{"POST", "/api/vault/browser", true},
		{"GET", "/api/vault", false},
		{"POST", "/api/browser/extensions/upload", true},
		{"GET", "/api/browser/files/saved", false},
		{"GET", "/api/browser/files/downloads/01234567-89ab-cdef-0123-456789abcdef", true},
		{"GET", "/api/browser/files/downloads/01234567-89ab-cdef-0123-456789abcdef/content", true},
		{"POST", "/api/browser/files/uploads/01234567-89ab-cdef-0123-456789abcdef", true},
		{"GET", "/api/browser/tabs", false},
		{"POST", "/api/browser/new", false},
		{"GET", "/api/status", false},
		{"GET", "/", false},
		{"GET", "/api/claude-desktops", false},
	} {
		if got := microVMUnsupportedRoute(tc.method, tc.path); got != tc.want {
			t.Errorf("microVMUnsupportedRoute(%s %s) = %v, want %v", tc.method, tc.path, got, tc.want)
		}
	}
}
