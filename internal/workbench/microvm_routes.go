package workbench

import (
	"net/http"
	"strings"
)

// The MicroVM development image is minimal: it carries no cage guest helpers
// (cage-claude-account, cage-claude-desktop, the vault browser service) and no
// Lima browser controller. These routes always reached those components in the
// Lima Linux guest, so under MicroVM they would only fail with misleading
// maintenance errors. Every other workbench route works unchanged.
func microVMUnsupportedRoute(method, path string) bool {
	if !strings.HasPrefix(path, "/api/") {
		return false
	}
	switch {
	case path == "/api/claude-accounts":
		// The panel itself stays readable: it reports the limitation instead.
		return method != http.MethodGet
	case path == "/api/claude-desktop" || strings.HasPrefix(path, "/api/claude-desktop/"),
		path == "/api/vault/browser",
		path == "/api/browser/extensions/upload",
		strings.HasPrefix(path, "/api/browser/files/uploads/"),
		strings.HasPrefix(path, "/api/browser/files/downloads/"):
		return true
	}
	return false
}
