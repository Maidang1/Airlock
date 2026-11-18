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

type tailscaleState struct {
	mu          sync.Mutex
	sshKeyInput string
	serviceHost string
	servicePort string
	busy        bool
	errMsg      string
}

func (r *RootView) renderTailscaleView(c *ui.Context, pal Palette, snap core.Snapshot) {
	st := &r.tailscale
	ts := snap.Tailscale
	st.mu.Lock()
	busy := st.busy
	errMsg := st.errMsg
	st.mu.Unlock()

	PageScroll(c, pal, 980, 16, func() {
		ui.Column(c).Gap(14).FillWidth().Children(func() {
				Card(c, pal, func() {
					ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
						SectionHeading(c, pal, "Tailscale 私网组网", "所有节点流量仍受出口策略约束，支持私网设备服务访问与 SSH 密钥管理")
						if ts.Connected {
							PillBadge(c, "已连接", pal.Success, pal.SuccessSoft)
						} else if ts.Installed {
							PillBadge(c, "未连接", pal.Warning, pal.WarningSoft)
						} else {
							PillBadge(c, "未安装", pal.Muted, pal.ElevatedBg)
						}
					})
					ErrorBanner(c, pal, errMsg)
					ErrorBanner(c, pal, ts.Error)

					if ts.SelfName != "" || ts.SelfIP != "" {
						KeyValueRow(c, pal, "本机节点", fmt.Sprintf("%s (%s)", ts.SelfName, ts.SelfIP))
					}

					ui.Row(c).Gap(8).Wrap().Children(func() {
						if !ts.Installed {
							if ui.PrimaryButton(c, "安装 Tailscale").Disabled(busy || !snap.Status.Ready).Clicked() {
								go r.tailscaleAction("install", nil)
							}
						} else {
							if !ts.Connected {
								if ui.PrimaryButton(c, "发起登录").Disabled(busy || !snap.Status.Ready).Clicked() {
									go r.tailscaleAction("login", nil)
								}
								if ui.Button(c, "连接").Disabled(busy || !snap.Status.Ready).Clicked() {
									go r.tailscaleAction("connect", nil)
								}
							} else {
								if ui.Button(c, "断开连接").Disabled(busy).Clicked() {
									go r.tailscaleAction("disconnect", nil)
								}
								if ui.Button(c, "退出登录").Disabled(busy).Clicked() {
									go r.tailscaleAction("logout", nil)
								}
							}
						}
					})

					if ts.AuthURL != "" {
						ui.Row(c).Background(pal.AccentSoft).Border(1, pal.Accent).Radius(8).Padding(10).Gap(10).AlignItems(ui.Center).Children(func() {
							ui.Text(c, "请点击按钮在浏览器完成 Tailscale 授权登录："+ts.AuthURL).FontSize(12).TextColor(pal.Text).Grow(1).Selectable()
							if ui.PrimaryButton(c, "打开授权页").Clicked() && r.Bridge != nil {
								u := ts.AuthURL
								go func() { _ = r.Bridge.OpenExternal(u) }()
							}
						})
					}
				})

				// Open peer service & peers list
				Card(c, pal, func() {
					ui.Text(c, "私网设备与服务访问").FontSize(14).Bold().TextColor(pal.Text)
					ui.Row(c).Gap(8).FillWidth().Children(func() {
						ui.TextInput(c, &st.serviceHost).Placeholder("私网设备主机名或 Tailscale IP").Grow(1)
						ui.TextInput(c, &st.servicePort).Placeholder("端口（如 8080）").Width(130)
						if ui.Button(c, "在隔离浏览器打开").Disabled(busy || !ts.Connected).Clicked() {
							port, _ := strconv.Atoi(strings.TrimSpace(st.servicePort))
							host := strings.TrimSpace(st.serviceHost)
							go r.tailscaleAction("open-service", map[string]any{"host": host, "port": port})
						}
					})
					if len(ts.Peers) == 0 {
						ui.Text(c, "暂无在线或记录的私网节点").FontSize(12).TextColor(pal.Muted)
					}
					for _, peer := range ts.Peers {
						p := peer
						ui.Row(c).FillWidth().Background(pal.ElevatedBg).Border(1, pal.Border).Radius(6).Padding(8, 10).Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
							ui.Column(c).Gap(1).Children(func() {
								ui.Text(c, p.Name).FontSize(13).Bold().TextColor(pal.Text)
								ui.Text(c, strings.Join(p.IPs, ", ")+" · "+p.OS).FontSize(11).TextColor(pal.Muted)
							})
							ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
								if p.Online {
									PillBadge(c, "在线", pal.Success, pal.SuccessSoft)
								} else {
									PillBadge(c, "离线", pal.Muted, pal.CardBg)
								}
								if len(p.IPs) > 0 {
									if ui.Button(c, "填入地址").Clicked() {
										st.mu.Lock()
										st.serviceHost = p.IPs[0]
										st.mu.Unlock()
									}
								}
							})
						})
					}
				})

				// SSH Keys
				Card(c, pal, func() {
					ui.Text(c, "SSH 授权公钥管理").FontSize(14).Bold().TextColor(pal.Text)
					ui.Row(c).Gap(8).FillWidth().Children(func() {
						ui.TextInput(c, &st.sshKeyInput).Placeholder("粘贴 ssh-ed25519 / ssh-rsa 公钥…").Grow(1)
						if ui.Button(c, "添加公钥").Disabled(busy || strings.TrimSpace(st.sshKeyInput) == "").Clicked() {
							k := strings.TrimSpace(st.sshKeyInput)
							go r.tailscaleAction("add-key", map[string]any{"key": k})
						}
					})
					for _, key := range ts.Keys {
						k := key
						preview := k
						if len(preview) > 72 {
							preview = preview[:72] + "…"
						}
						ui.Row(c).FillWidth().Background(pal.ElevatedBg).Border(1, pal.Border).Radius(6).Padding(8, 10).Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
							ui.Text(c, preview).FontSize(12).TextColor(pal.Text).Grow(1)
							if ui.Button(c, "移除").Disabled(busy).Clicked() {
								go r.tailscaleAction("remove-key", map[string]any{"key": k})
							}
						})
					}
				})
			})
	})
}

func (r *RootView) tailscaleAction(action string, body map[string]any) {
	st := &r.tailscale
	st.mu.Lock()
	st.busy = true
	st.errMsg = ""
	st.mu.Unlock()
	r.Invalidate()

	if body == nil {
		body = map[string]any{}
	}
	err := r.Client.PostJSON(context.Background(), "/api/tailscale/"+action, body, nil)
	st.mu.Lock()
	st.busy = false
	if err != nil {
		st.errMsg = err.Error()
	} else if action == "add-key" {
		st.sshKeyInput = ""
	}
	st.mu.Unlock()
	r.Store.TriggerRefresh()
	r.Invalidate()
}
