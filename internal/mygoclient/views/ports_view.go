package views

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/egoist/mygo/ui"
	"github.com/sylvinus/agent-vm/internal/mygoclient/core"
)

func (r *RootView) effectiveProxyPort(snap core.Snapshot) int {
	if !snap.Proxy.Active && !snap.Proxy.Running &&
		snap.ClientPrefs.PreferredProxyPort >= 1024 && snap.ClientPrefs.PreferredProxyPort <= 65535 {
		return snap.ClientPrefs.PreferredProxyPort
	}
	if snap.Proxy.Port >= 1024 && snap.Proxy.Port <= 65535 {
		return snap.Proxy.Port
	}
	if snap.ClientPrefs.PreferredProxyPort >= 1024 && snap.ClientPrefs.PreferredProxyPort <= 65535 {
		return snap.ClientPrefs.PreferredProxyPort
	}
	return 17896
}

func (r *RootView) parseDesiredProxyPort(fallback int) int {
	st := &r.proxy
	st.mu.Lock()
	raw := strings.TrimSpace(st.portStr)
	st.mu.Unlock()
	if raw == "" {
		return fallback
	}
	p, err := strconv.Atoi(raw)
	if err != nil {
		return -1
	}
	return p
}

func (r *RootView) ensurePortCheck(snap core.Snapshot) {
	st := &r.proxy
	effective := r.effectiveProxyPort(snap)

	st.mu.Lock()
	if !st.portInit {
		st.portStr = strconv.Itoa(effective)
		st.portInit = true
	}
	raw := strings.TrimSpace(st.portStr)
	checkPort := effective
	if raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			checkPort = parsed
		} else {
			checkPort = -1
		}
	}
	needCheck := !st.portChecked || st.lastCheckedBackendPort != snap.Proxy.Port || st.lastCheckedRunning != snap.Proxy.Running
	if needCheck && !st.portChecking {
		st.portChecked = true
		st.portChecking = true
		st.lastCheckedBackendPort = snap.Proxy.Port
		st.lastCheckedRunning = snap.Proxy.Running
	} else {
		needCheck = false
	}
	st.mu.Unlock()

	if needCheck {
		go r.runPortConflictCheck(checkPort)
	}
}

func (r *RootView) runPortConflictCheck(port int) {
	snap := r.Store.Snapshot()
	if port == 0 {
		port = r.effectiveProxyPort(snap)
	}
	rep := core.InspectHostPorts(port, snap, r.Client.Origin())
	st := &r.proxy
	st.mu.Lock()
	st.portReport = &rep
	st.portChecking = false
	st.lastCheckedBackendPort = snap.Proxy.Port
	st.lastCheckedRunning = snap.Proxy.Running
	st.mu.Unlock()
	r.Invalidate()
}

func (r *RootView) autoAssignFreeProxyPort() {
	snap := r.Store.Snapshot()
	startFrom := 17897
	if cur := r.parseDesiredProxyPort(17896); cur >= 17896 && cur < 17999 {
		startFrom = cur + 1
	}
	freePort, err := core.SuggestFreeProxyPort(startFrom, snap.Proxy.Port, snap.Proxy.Running)
	if err != nil {
		st := &r.proxy
		st.mu.Lock()
		st.errMsg = "未能找到空闲端口：" + err.Error()
		st.mu.Unlock()
		r.Invalidate()
		return
	}
	st := &r.proxy
	st.mu.Lock()
	st.portStr = strconv.Itoa(freePort)
	st.portInit = true
	st.mu.Unlock()
	r.applyDesiredProxyPort(freePort, true)
}

func (r *RootView) applyDesiredProxyPort(port int, autoAssigned bool) {
	st := &r.proxy
	if port < 1024 || port > 65535 {
		st.mu.Lock()
		st.errMsg = "代理端口须在 1024–65535 之间（避开系统特权端口）"
		st.notice = ""
		st.mu.Unlock()
		r.Invalidate()
		return
	}

	snap := r.Store.Snapshot()
	selfOwned := snap.Proxy.Running && snap.Proxy.Port == port
	chk := core.CheckLoopbackPort(port, selfOwned)
	if chk.State == core.PortStateConflict {
		rep := core.InspectHostPorts(port, snap, r.Client.Origin())
		st.mu.Lock()
		st.errMsg = "端口冲突：" + chk.Detail
		st.notice = ""
		st.portReport = &rep
		st.portChecked = true
		st.portChecking = false
		st.mu.Unlock()
		r.Invalidate()
		return
	}

	r.Store.UpdateClientPreferences(func(p *core.ClientPreferences) {
		p.PreferredProxyPort = port
	})

	st.mu.Lock()
	st.busy = true
	st.errMsg = ""
	st.notice = ""
	if snap.Status.Job.Error != "" && strings.Contains(snap.Status.Job.Error, "端口") {
		st.dismissedJobErr = snap.Status.Job.Error
	}
	st.mu.Unlock()
	r.onboarding.mu.Lock()
	r.onboarding.errMsg = ""
	r.onboarding.mu.Unlock()
	r.Invalidate()

	var err error
	if snap.Proxy.Selected != "" {
		_, err = r.Store.RunTrackedJob(context.Background(), "/api/proxy/restart", map[string]any{"port": port})
	} else {
		_, err = r.Store.RunTrackedJob(context.Background(), "/api/proxy/port", map[string]any{"port": port})
		var httpErr *core.HTTPError
		if errors.As(err, &httpErr) && httpErr.StatusCode == 404 {
			// Fallback gracefully when attached to a running helper built before /api/proxy/port.
			err = nil
		}
	}

	latestSnap := r.Store.Snapshot()
	rep := core.InspectHostPorts(port, latestSnap, r.Client.Origin())

	st.mu.Lock()
	st.busy = false
	st.portReport = &rep
	st.portChecked = true
	st.portChecking = false
	st.lastCheckedBackendPort = latestSnap.Proxy.Port
	st.lastCheckedRunning = latestSnap.Proxy.Running
	if err != nil {
		st.errMsg = err.Error()
	} else if autoAssigned {
		st.notice = fmt.Sprintf("已自动分配并保存空闲代理端口 127.0.0.1:%d（无宿主机冲突）", port)
	} else {
		st.notice = fmt.Sprintf("已保存并应用独立代理端口 127.0.0.1:%d（无宿主机冲突）", port)
	}
	st.mu.Unlock()
	r.Invalidate()
}

func renderPortStatusBadge(c *ui.Context, pal Palette, res core.PortCheckResult) {
	switch res.State {
	case core.PortStateAvailable:
		PillBadge(c, fmt.Sprintf("✓ 端口 %d 空闲可用", res.Port), pal.Success, pal.SuccessSoft)
	case core.PortStateSelfRunning:
		if res.Port > 0 {
			PillBadge(c, fmt.Sprintf("● 端口 %d 运行中", res.Port), pal.Accent, pal.AccentSoft)
		} else {
			PillBadge(c, "● 动态端口运行中", pal.Accent, pal.AccentSoft)
		}
	case core.PortStateConflict:
		PillBadge(c, fmt.Sprintf("✕ 端口 %d 冲突", res.Port), pal.Danger, pal.DangerSoft)
	default:
		PillBadge(c, "端口无效", pal.Warning, pal.WarningSoft)
	}
}

func (r *RootView) renderPortConfigBar(c *ui.Context, pal Palette, snap core.Snapshot, compact bool) {
	r.ensurePortCheck(snap)
	st := &r.proxy

	st.mu.Lock()
	busy := st.busy || snap.Status.Job.Running
	checking := st.portChecking
	rep := st.portReport
	showDetails := st.showPortDetails
	st.mu.Unlock()

	ui.Column(c).Gap(8).FillWidth().Background(pal.ElevatedBg).Border(1, pal.Border).Radius(8).Padding(10, 12).Children(func() {
		ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Gap(10).Children(func() {
			ui.Column(c).Gap(1).Grow(1).Shrink(1).Children(func() {
				ui.Text(c, "独立代理端口（127.0.0.1 回环防冲突）").FontSize(13).Bold().TextColor(pal.Text)
				ui.Text(c, "当宿主机已运行代理或另一份 Cage 实例时，可自定义独立端口避免冲突").FontSize(11).TextColor(pal.Muted)
			})
			if checking && rep == nil {
				PillBadge(c, "正在检测…", pal.Muted, pal.CardBg)
			} else if rep != nil {
				renderPortStatusBadge(c, pal, rep.CandidateProxy)
			}
		})

		InsetDivider(c, pal)

		ui.Row(c).Gap(6).Wrap().AlignItems(ui.Center).Children(func() {
			ui.Text(c, "127.0.0.1 :").FontSize(12).Bold().TextColor(pal.Muted)
			inp := ui.TextInput(c, &st.portStr).Label("代理端口").Placeholder("1024-65535").Width(84)
			if inp.Changed() {
				st.mu.Lock()
				st.portChecked = false
				st.mu.Unlock()
			}
			if inp.Submitted() && !busy {
				if port, err := strconv.Atoi(strings.TrimSpace(st.portStr)); err == nil {
					go r.applyDesiredProxyPort(port, false)
				}
			}
			if ui.PrimaryButton(c, "应用端口").Disabled(busy).Clicked() {
				if port, err := strconv.Atoi(strings.TrimSpace(st.portStr)); err == nil {
					go r.applyDesiredProxyPort(port, false)
				} else {
					st.mu.Lock()
					st.errMsg = "请输入 1024–65535 之间的有效端口号"
					st.mu.Unlock()
				}
			}
			if ui.Button(c, "自动分配空闲端口").Disabled(checking || busy).Clicked() {
				go r.autoAssignFreeProxyPort()
			}
			if ui.Button(c, "检测冲突").Disabled(checking || busy).Clicked() {
				port := r.parseDesiredProxyPort(r.effectiveProxyPort(snap))
				st.mu.Lock()
				st.portChecking = true
				st.mu.Unlock()
				go r.runPortConflictCheck(port)
			}
			if compact {
				label := "宿主机端口详情"
				if showDetails {
					label = "收起详情"
				}
				if ui.Button(c, label).Clicked() {
					st.mu.Lock()
					st.showPortDetails = !st.showPortDetails
					st.mu.Unlock()
				}
			}
		})

		if rep != nil && (rep.CandidateProxy.State == core.PortStateConflict || rep.CandidateProxy.State == core.PortStateInvalid) {
			msg := rep.CandidateProxy.Detail
			if rep.CandidateProxy.State == core.PortStateConflict && rep.SuggestedPort > 0 {
				msg += fmt.Sprintf("；建议改用空闲端口 %d（点击「自动分配空闲端口」一键切换）", rep.SuggestedPort)
			}
			ui.Text(c, msg).FontSize(12).TextColor(pal.Danger)
		}

		if (!compact || showDetails) && rep != nil {
			r.renderPortReportTables(c, pal, rep)
		}
	})
}

func (r *RootView) renderPortReportTables(c *ui.Context, pal Palette, rep *core.HostPortReport) {
	if rep == nil {
		return
	}
	ui.Column(c).Gap(8).FillWidth().Padding(4, 0, 0, 0).Children(func() {
		ui.Text(c, "Cage 本机回环服务端口").FontSize(12).Bold().TextColor(pal.Text)
		if len(rep.CageServices) > 0 {
			ui.Column(c).FillWidth().Background(pal.CardBg).Border(1, pal.Border).Radius(8).Gap(0).Children(func() {
				for idx, svc := range rep.CageServices {
					if idx > 0 {
						InsetDivider(c, pal)
					}
					item := svc
					ui.Row(c).FillWidth().Padding(8, 10).Justify(ui.SpaceBetween).AlignItems(ui.Center).Gap(8).Children(func() {
						ui.Column(c).Gap(1).Children(func() {
							ui.Text(c, item.Name+" · "+item.Address).FontSize(12).Bold().TextColor(pal.Text)
							ui.Text(c, item.Detail).FontSize(11).TextColor(pal.Muted)
						})
						renderPortStatusBadge(c, pal, item)
					})
				}
			})
		}

		ui.Text(c, "宿主机常见代理端口占用扫描").FontSize(12).Bold().TextColor(pal.Text)
		if len(rep.HostListeners) == 0 {
			ui.Text(c, "未检测到常见宿主机代理端口占用（7890 / 7891 / 7897 / 1080 / 1087 / 9090 / 17896）").FontSize(11).TextColor(pal.Muted)
		} else {
			ui.Column(c).FillWidth().Background(pal.CardBg).Border(1, pal.Border).Radius(8).Gap(0).Children(func() {
				for idx, hl := range rep.HostListeners {
					if idx > 0 {
						InsetDivider(c, pal)
					}
					item := hl
					ui.Row(c).FillWidth().Padding(8, 10).Justify(ui.SpaceBetween).AlignItems(ui.Center).Gap(8).Children(func() {
						ui.Column(c).Gap(1).Children(func() {
							ui.Text(c, item.Address+" · "+item.Name).FontSize(12).Bold().TextColor(pal.Text)
							ui.Text(c, item.Detail).FontSize(11).TextColor(pal.Muted)
						})
						PillBadge(c, "已被占用", pal.Warning, pal.WarningSoft)
					})
				}
			})
		}
	})
}

func (r *RootView) renderPortConflictSettingsTab(c *ui.Context, pal Palette, snap core.Snapshot) {
	r.ensurePortCheck(snap)
	st := &r.proxy

	st.mu.Lock()
	busy := st.busy || snap.Status.Job.Running
	checking := st.portChecking
	errMsg := st.errMsg
	notice := st.notice
	rep := st.portReport
	st.mu.Unlock()

	Card(c, pal, func() {
		ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
			SectionHeading(c, pal, "端口配置与宿主机冲突检测", "为内置 Mihomo 核心指定独立的 127.0.0.1 端口，避免与宿主机 Clash / 其他 Cage 实例冲突")
			if ui.Button(c, "重新扫描宿主机端口").Disabled(checking || busy).Clicked() {
				port := r.parseDesiredProxyPort(r.effectiveProxyPort(snap))
				st.mu.Lock()
				st.portChecking = true
				st.mu.Unlock()
				go r.runPortConflictCheck(port)
			}
		})
		ErrorBanner(c, pal, errMsg)
		InfoBanner(c, pal, notice)
		r.renderPortConfigBar(c, pal, snap, false)
	})

	Card(c, pal, func() {
		ui.Text(c, "端口冲突处理说明").FontSize(14).Bold().TextColor(pal.Text)
		ui.Text(c, "• 开发模式（Cage Dev）与正式版（/Applications/Cage.app）若同时运行，默认都会尝试使用 17896，可在上方一键分配独立空闲端口。").FontSize(12).TextColor(pal.Muted)
		ui.Text(c, "• 控制台 API 与 Lima 客机 SSH 均由系统自动绑定 127.0.0.1 动态空闲端口，不会与宿主机常规服务冲突。").FontSize(12).TextColor(pal.Muted)
		if rep != nil && !rep.CheckedAt.IsZero() {
			ui.Text(c, "最近扫描时间："+rep.CheckedAt.Local().Format("15:04:05")).FontSize(11).TextColor(pal.Muted)
		}
	})
}
