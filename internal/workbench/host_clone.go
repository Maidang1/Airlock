package workbench

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"

	"github.com/sylvinus/agent-vm/internal/host"
	"github.com/sylvinus/agent-vm/internal/hostimage"
	"github.com/sylvinus/agent-vm/internal/hostproxy"
)

func (s *Server) hostClonePlan(ctx context.Context) (hostimage.Plan, error) {
	identity, err := hostimage.Host(ctx)
	if err != nil {
		return hostimage.Plan{}, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return hostimage.Plan{}, err
	}
	cache := filepath.Join(s.profile, "state", "guest-media", identity.System)
	transport := hostproxy.NewTransport()
	defer transport.CloseIdleConnections()
	return hostimage.Resolve(ctx, identity, cache, filepath.Join(home, "Downloads"), nil, transport)
}

func hostCloneSystemReserve(plan hostimage.Plan) int {
	// Intel stages the full installer inside the new disk in addition to the
	// installed system and development tools.
	if plan.Host.System == "macos" && plan.Host.Arch == "amd64" {
		return 64
	}
	return 32
}

func (s *Server) hostClone(w http.ResponseWriter, r *http.Request) {
	if !s.hasTrustedView(r) || !s.localVMStorage() {
		http.Error(w, "请在本机 Cage 窗口选择系统创建方式", http.StatusForbidden)
		return
	}
	view, err := s.guestInstallView()
	if err != nil {
		requestError(w, err)
		return
	}
	if !view.Available || (!view.Fresh && view.Pending == nil) {
		requestError(w, errors.New("当前环境无法创建新的本机系统"))
		return
	}
	var plan hostimage.Plan
	if view.Pending != nil && view.Pending.Source.Host.System != "" {
		plan = view.Pending.Source
	} else {
		plan, err = s.hostClonePlan(r.Context())
		if err != nil {
			requestError(w, err)
			return
		}
	}
	minimumGiB := hostCloneSystemReserve(plan)
	started := view.Pending != nil && !view.Reselect
	if started {
		minimumGiB = 0
	}
	downloadBytes := plan.RemainingBytes(filepath.Join(s.profile, "state", "guest-media", plan.Host.System))
	minimumGiB += int((downloadBytes + (1 << 30) - 1) / (1 << 30))
	freeGiB, measured := host.FreeGiB(filepath.Join(s.profile, "state"))
	jsonReply(w, map[string]any{
		"plan": plan, "key": plan.Key(), "download_bytes": downloadBytes,
		"minimum_free_gib": minimumGiB, "free_gib": freeGiB,
		"installation_started": started,
		"enough_space":         !measured || freeGiB >= minimumGiB,
	})
}
