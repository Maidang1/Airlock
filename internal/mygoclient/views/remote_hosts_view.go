package views

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/sylvinus/agent-vm/internal/mygoclient/core"
)

type remoteHostsState struct {
	mu          sync.Mutex
	form        core.HostProfile
	portStr     string
	passphrase  string
	deploy      bool
	localAction string // "auto" | "keep" | "stop"
	busy        bool
	errMsg      string
}

func (r *RootView) renderRemoteHostsView(c *ui.Context, pal Palette, snap core.Snapshot) {
	st := &r.remoteHosts
	st.mu.Lock()
	if st.localAction == "" {
		st.localAction = "auto"
	}
	busy := st.busy
	errMsg := st.errMsg
	editing := st.form.ID != ""
	st.mu.Unlock()

	ctxState := snap.Context

	PageScroll(c, pal, 980, 16, func() {
		ui.Column(c).Gap(14).FillWidth().Children(func() {
				// Current connection status
				Card(c, pal, func() {
					ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
						SectionHeading(c, pal, "远程 Cloud SSH 与工作台切换", "安全连接远端 Linux / Windows Cloud SSH 主机（端口转发与工作流隔离）")
						if ctxState.Mode == "remote" {
							PillBadge(c, "已连接远程主机", pal.Accent, pal.AccentSoft)
						} else {
							PillBadge(c, "本机工作模式", pal.Success, pal.SuccessSoft)
						}
					})
					ErrorBanner(c, pal, errMsg)
					KeyValueRow(c, pal, "当前模式", fmt.Sprintf("%s (%s · %s)", ctxState.Mode, ctxState.Name, ctxState.ServerPlatform))
					if ctxState.HostFingerprint != "" {
						KeyValueRow(c, pal, "主机公钥指纹", ctxState.HostFingerprint)
					}

					ui.Row(c).Gap(8).Wrap().Children(func() {
						if ctxState.Mode != "local" && !ctxState.ClientOnly {
							localLabel := "切回本机环境"
							if ctxState.Mode == "none" {
								localLabel = "连接本机环境"
							}
							if ui.PrimaryButton(c, localLabel).Disabled(busy).Clicked() {
								go r.switchToLocal(ctxState)
							}
						}
						if ctxState.Mode == "remote" {
							if ui.Button(c, "断开当前远程连接").Disabled(busy).Clicked() {
								go r.disconnectRemote(ctxState)
							}
						}
					})
				})

				// Pending desktop-gateway candidate approval card
				if cand := ctxState.Candidate; cand != nil {
					Card(c, pal, func() {
						ui.Text(c, "待切换工作台候选连接").FontSize(14).Bold().TextColor(pal.Text)
						KeyValueRow(c, pal, "目标模式", cand.Mode+" · "+cand.Connection.State)
						if cand.Connection.Phase != "" || cand.Connection.Message != "" {
							ui.Text(c, cand.Connection.Phase+" "+cand.Connection.Message).FontSize(12).TextColor(pal.Muted)
						}
						if snap.LocalStatus.Running && cand.Mode == "remote" {
							ui.Text(c, "切换时如何处理当前本机环境：").FontSize(12).TextColor(pal.Muted)
							ui.Select(c, &st.localAction, []string{"auto", "keep", "stop"})
						}
						ui.Row(c).Gap(8).Children(func() {
							if cand.Connection.State == "connected" || cand.Mode == "local" {
								if ui.PrimaryButton(c, "确认切换到此环境").Disabled(busy).Clicked() {
									go r.commitGatewayCandidate(cand.ID, snap.LocalStatus.ControllerID)
								}
							}
							if ui.Button(c, "取消切换").Disabled(busy).Clicked() {
								go r.postSimple("/api/client/cancel", map[string]string{"candidate_id": cand.ID})
							}
						})
					})
				}

				// Saved remote hosts list
				Card(c, pal, func() {
					ui.Text(c, "已保存的 Cloud SSH 主机").FontSize(14).Bold().TextColor(pal.Text)
					ui.Row(c).Gap(8).Wrap().AlignItems(ui.Center).Children(func() {
						ui.TextInput(c, &st.passphrase).Password().Placeholder("可选 SSH 私钥口令").Width(200)
						ui.Checkbox(c, &st.deploy, "自动部署 / 更新远端 Airlock Server")
					})

					if len(snap.Hosts) == 0 {
						ui.Text(c, "尚未保存远程 Cloud SSH 主机，可在下方新增。").FontSize(12).TextColor(pal.Muted)
					}
					for _, h := range snap.Hosts {
						host := h
						ui.Row(c).FillWidth().Background(pal.ElevatedBg).Border(1, pal.Border).Radius(6).Padding(10).Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
							ui.Column(c).Gap(2).Children(func() {
								ui.Text(c, host.Name).FontSize(13).Bold().TextColor(pal.Text)
								ui.Text(c, fmt.Sprintf("%s@%s:%d · %s · 认证:%s", host.User, host.Host, host.Port, host.Platform, host.Auth.Kind)).FontSize(11).TextColor(pal.Muted)
							})
							ui.Row(c).Gap(6).Children(func() {
								if ctxState.HostID == host.ID {
									PillBadge(c, "当前连接", pal.Accent, pal.AccentSoft)
								} else if ui.PrimaryButton(c, "连接").Disabled(busy).Clicked() {
									go r.connectHost(ctxState.DesktopGateway, host.ID)
								}
								if ui.Button(c, "编辑").Clicked() {
									st.mu.Lock()
									st.form = host
									st.portStr = fmt.Sprintf("%d", host.Port)
									st.mu.Unlock()
								}
								if ui.Button(c, "删除").Disabled(busy || ctxState.HostID == host.ID).Clicked() {
									id := host.ID
									go func() {
										_ = r.Client.DeleteJSON(context.Background(), "/api/client/hosts/"+id, nil)
										r.Store.TriggerRefresh()
									}()
								}
							})
						})
					}
				})

				// Add / edit host profile form
				Card(c, pal, func() {
					title := "新增 Cloud SSH 主机"
					if editing {
						title = "编辑主机：" + st.form.Name
					}
					ui.Text(c, title).FontSize(14).Bold().TextColor(pal.Text)
					ui.Row(c).Gap(8).FillWidth().Children(func() {
						ui.TextInput(c, &st.form.Name).Placeholder("名称（如 Tokyo Cloud）").Grow(1)
						ui.TextInput(c, &st.form.Host).Placeholder("主机域名或 IP").Grow(1)
						ui.TextInput(c, &st.portStr).Placeholder("端口").Width(80)
					})
					ui.Row(c).Gap(8).FillWidth().Children(func() {
						ui.TextInput(c, &st.form.User).Placeholder("SSH 用户名").Width(160)
						ui.Select(c, &st.form.Platform, []string{"linux", "windows"})
						ui.Select(c, &st.form.Auth.Kind, []string{"agent", "key"})
						if st.form.Auth.Kind == "key" {
							ui.TextInput(c, &st.form.Auth.KeyPath).Placeholder("私钥路径（如 ~/.ssh/id_ed25519）").Grow(1)
						}
					})
					ui.Row(c).Gap(8).Children(func() {
						if ui.PrimaryButton(c, "保存主机配置").Disabled(busy).Clicked() {
							go r.saveHostProfile()
						}
						if editing {
							if ui.Button(c, "取消编辑").Clicked() {
								st.mu.Lock()
								st.form = core.HostProfile{Port: 22, Platform: "linux", Auth: core.HostAuthConfig{Kind: "agent"}}
								st.portStr = "22"
								st.mu.Unlock()
							}
						}
					})
				})
		})
	})
}

func (r *RootView) saveHostProfile() {
	st := &r.remoteHosts
	st.mu.Lock()
	form := st.form
	if p, err := strconv.Atoi(strings.TrimSpace(st.portStr)); err == nil && p > 0 {
		form.Port = p
	}
	if form.Port <= 0 {
		form.Port = 22
	}
	st.busy = true
	st.errMsg = ""
	st.mu.Unlock()
	r.Invalidate()

	var err error
	if form.ID != "" {
		err = r.Client.PutJSON(context.Background(), "/api/client/hosts/"+form.ID, form, nil)
	} else {
		err = r.Client.PostJSON(context.Background(), "/api/client/hosts", form, nil)
	}
	st.mu.Lock()
	st.busy = false
	if err != nil {
		st.errMsg = err.Error()
	} else {
		st.form = core.HostProfile{Port: 22, Platform: "linux", Auth: core.HostAuthConfig{Kind: "agent"}}
		st.portStr = "22"
	}
	st.mu.Unlock()
	r.Store.TriggerRefresh()
	r.Invalidate()
}

func (r *RootView) connectHost(gateway bool, hostID string) {
	st := &r.remoteHosts
	st.mu.Lock()
	pass := st.passphrase
	dep := st.deploy
	st.busy = true
	st.errMsg = ""
	st.mu.Unlock()
	r.Invalidate()

	path := "/api/client/connect"
	payload := map[string]any{
		"host_id":    hostID,
		"passphrase": pass,
		"deploy":     dep,
	}
	if gateway {
		path = "/api/client/candidate"
		payload["mode"] = "remote"
	}
	err := r.Client.PostJSON(context.Background(), path, payload, nil)
	st.mu.Lock()
	st.busy = false
	if err != nil {
		st.errMsg = err.Error()
	} else {
		st.passphrase = ""
	}
	st.mu.Unlock()
	r.Store.TriggerRefresh()
	r.Invalidate()
}

func (r *RootView) commitGatewayCandidate(candID, localCtrlID string) {
	st := &r.remoteHosts
	st.mu.Lock()
	act := st.localAction
	st.busy = true
	st.errMsg = ""
	st.mu.Unlock()

	err := r.Client.PostJSON(context.Background(), "/api/client/commit", map[string]any{
		"candidate_id":        candID,
		"local_action":        act,
		"local_controller_id": localCtrlID,
	}, nil)
	st.mu.Lock()
	st.busy = false
	if err != nil {
		st.errMsg = err.Error()
	}
	st.mu.Unlock()
	r.Store.TriggerRefresh()
	r.Invalidate()
}

func (r *RootView) switchToLocal(ctxState core.ClientContext) {
	if ctxState.DesktopGateway {
		if err := r.Store.ConnectGatewayLocal(context.Background()); err != nil {
			r.PushToast("连接本机环境失败：" + err.Error())
		} else {
			r.Store.TriggerRefresh()
			r.Invalidate()
		}
		return
	}
	r.postSimple("/api/client/local", map[string]any{"connection_id": ctxState.ConnectionID})
}

func (r *RootView) disconnectRemote(ctxState core.ClientContext) {
	var next core.ClientContext
	if err := r.Client.PostJSON(context.Background(), "/api/client/disconnect", map[string]any{"connection_id": ctxState.ConnectionID}, &next); err != nil {
		r.PushToast(err.Error())
		return
	}
	if next.ConnectionID != "" {
		r.Store.MarkStayDisconnected(next.ConnectionID)
	}
	r.Store.TriggerRefresh()
	r.Invalidate()
}
