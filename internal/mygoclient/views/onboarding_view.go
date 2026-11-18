package views

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/sylvinus/agent-vm/internal/mygoclient/core"
	"github.com/sylvinus/agent-vm/internal/mygoclient/surfaces"
)

type guestOptionsView struct {
	LinuxSystems  []string `json:"linux_systems"`
	HostMatchOnly bool     `json:"host_match_only"`
	Reselect      bool     `json:"reselect"`
	System        string   `json:"system"`
	Available     bool     `json:"available"`
	Reason        string   `json:"reason"`
	Fresh         bool     `json:"fresh"`
	Activate      bool     `json:"activate"`
	Pending       *struct {
		System string `json:"system"`
		ID     string `json:"id"`
	} `json:"pending"`
}

type macSetupPermView struct {
	Screen                 bool   `json:"screen"`
	Input                  bool   `json:"input"`
	Projects               bool   `json:"projects"`
	ProjectInstallRequired bool   `json:"project_install_required"`
	ProjectsError          string `json:"projects_error"`
	Sharing                bool   `json:"sharing"`
	SharingSummary         string `json:"sharing_summary"`
	ManagedCredential      bool   `json:"managed_credential"`
}

type onboardingState struct {
	mu           sync.Mutex
	guestOpts    *guestOptionsView
	selectedOS   string
	mediaURL     string
	mediaSHA256  string
	editorTerms  bool
	busy         bool
	errMsg       string
	notice       string
	macPerms     *macSetupPermView
	setupSurface *surfaces.MacOSLoginSurface
	loadedOpts   bool
}

func (r *RootView) ensureOnboardingData(snap core.Snapshot) {
	st := &r.onboarding
	st.mu.Lock()
	needOpts := !st.loadedOpts
	if needOpts {
		st.loadedOpts = true
	}
	if st.setupSurface == nil && snap.Account.InitialMacSetup {
		st.setupSurface = surfaces.NewMacOSLoginSurface(r.Client, "/api/macos/first-setup", "", r.Invalidate)
		go func(surf *surfaces.MacOSLoginSurface) {
			_ = surf.Open(context.Background())
		}(st.setupSurface)
	}
	st.mu.Unlock()

	if needOpts {
		go r.refreshOnboardingOptions()
	}
}

func (r *RootView) refreshOnboardingOptions() {
	var opts guestOptionsView
	if err := r.Client.GetJSON(context.Background(), "/api/environment/guest-systems", &opts); err == nil {
		st := &r.onboarding
		st.mu.Lock()
		st.guestOpts = &opts
		if st.selectedOS == "" {
			if opts.Pending != nil && opts.Pending.System != "" {
				st.selectedOS = opts.Pending.System
			} else if opts.System != "" && opts.Available {
				st.selectedOS = opts.System
			} else if len(opts.LinuxSystems) > 0 {
				st.selectedOS = opts.LinuxSystems[0]
			}
		}
		st.mu.Unlock()
		r.Invalidate()
	}
	if r.Store.Snapshot().Account.InitialMacSetup {
		var perm macSetupPermView
		if err := r.Client.PostJSON(context.Background(), "/api/macos/first-setup", map[string]string{"action": "permissions"}, &perm); err == nil {
			r.onboarding.mu.Lock()
			r.onboarding.macPerms = &perm
			r.onboarding.mu.Unlock()
			r.Invalidate()
		}
	}
}

func (r *RootView) renderOnboarding(c *ui.Context, pal Palette, snap core.Snapshot) {
	r.ensureOnboardingData(snap)
	st := &r.onboarding
	st.mu.Lock()
	opts := st.guestOpts
	busy := st.busy
	errMsg := st.errMsg
	notice := st.notice
	perms := st.macPerms
	surf := st.setupSurface
	st.mu.Unlock()

	PageScroll(c, pal, 940, 16, func() {
		ui.Column(c).Gap(12).FillWidth().Children(func() {
			TitleBarClearance(c)
			ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
				SectionHeading(c, pal, "初始化 Cage 隔离工作环境", "按向导完成客机部署、网络代理与安全验证后即可使用")
				ui.Row(c).Gap(8).Children(func() {
					if ui.Button(c, "刷新状态").Clicked() {
						go r.refreshOnboardingOptions()
						r.Store.TriggerRefresh()
					}
					if ui.Button(c, "导出诊断包").Clicked() {
						r.OpenDiagnosticsExport()
					}
				})
			})

			ErrorBanner(c, pal, errMsg)
			InfoBanner(c, pal, notice)

			// Step 1: Guest system choice
			Card(c, pal, func() {
				ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "1. 选择隔离客机系统").FontSize(15).Bold().TextColor(pal.Text)
					if snap.Status.GuestSystem != "" {
						PillBadge(c, "当前客机："+snap.Status.GuestSystem, pal.Accent, pal.AccentSoft)
					}
				})
				if opts != nil {
					var choices []string
					if opts.System != "" && opts.Available {
						choices = append(choices, opts.System)
					}
					choices = append(choices, opts.LinuxSystems...)
					if len(choices) > 0 && opts.Pending == nil {
						ui.Row(c).Gap(10).AlignItems(ui.Center).FillWidth().Children(func() {
							ui.Text(c, "客机系统镜像").FontSize(12).TextColor(pal.Muted)
							ui.Select(c, &st.selectedOS, choices)
						})
					} else if opts.Reason != "" {
						ui.Text(c, opts.Reason).FontSize(12).TextColor(pal.Warning)
					}
				}
			})

			// Step 2: Port conflict prevention, proxy configuration & exit node selection
			Card(c, pal, func() {
				ui.Text(c, "2. 端口防冲突与网络代理配置").FontSize(15).Bold().TextColor(pal.Text)
				KeyValueRow(c, pal, "当前代理状态", onboardingProxySummary(snap.Proxy, r.effectiveProxyPort(snap)))
				r.renderPortConfigBar(c, pal, snap, true)
				r.renderProxySourcesAndImport(c, pal, snap, true)
			})

			// Step 3 (macOS guest only): Interactive desktop & host permissions
			if snap.Account.InitialMacSetup {
				Card(c, pal, func() {
					ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
						ui.Text(c, "3. 完成 macOS 客机系统权限确认").FontSize(15).Bold().TextColor(pal.Text)
						if surf != nil {
							if ui.Button(c, "自动填入客机密码").Clicked() {
								go func() {
									if err := surf.FillPassword(context.Background()); err != nil {
										r.PushToast("自动填入失败：" + err.Error())
									}
								}()
							}
						}
					})
					if perms != nil {
						ui.Row(c).Gap(8).Wrap().Children(func() {
							permBadge(c, pal, "窗口录制", perms.Screen)
							permBadge(c, pal, "辅助功能输入", perms.Input)
							permBadge(c, pal, "文件系统扩展", perms.Projects)
							permBadge(c, pal, "屏幕共享检查", perms.Sharing)
						})
						if perms.ProjectsError != "" {
							ErrorBanner(c, pal, perms.ProjectsError)
						}
						if perms.SharingSummary != "" {
							ui.Text(c, perms.SharingSummary).FontSize(12).TextColor(pal.Muted)
						}
					}
					ui.Row(c).Gap(8).Wrap().Children(func() {
						for _, pair := range [][2]string{
							{"打开屏幕录制设置", "screen"},
							{"打开辅助功能设置", "input"},
							{"检查扩展与项目目录", "verify-projects"},
							{"确认屏幕共享已关闭", "confirm-sharing"},
						} {
							p := pair
							if ui.Button(c, p[0]).Clicked() {
								go r.macFirstSetupAction(p[1])
							}
						}
						if ui.PrimaryButton(c, "完成首次权限设置").Disabled(busy).Clicked() {
							go r.macFirstSetupAction("finish")
						}
					})
					if surf != nil {
						ui.Box(c).FillWidth().Height(460).Radius(8).Border(1, pal.Border).Children(func() {
							surf.View(c)
						})
					}
				})
			}

			// Final Step: Start VM, run safety check & enter workbench
			Card(c, pal, func() {
				finalTitle := "3. 安装启动、外网安全检查与进入工作台"
				if snap.Account.InitialMacSetup {
					finalTitle = "4. 安装启动、外网安全检查与进入工作台"
				}
				ui.Text(c, finalTitle).FontSize(15).Bold().TextColor(pal.Text)
				KeyValueRow(c, pal, "安全门控", safetyStatusLabel(snap))
				if snap.Status.Job.Running && snap.Status.Job.Kind != "proxy" && snap.Status.Job.Kind != "proxy-update" {
					ui.Column(c).Gap(6).FillWidth().Children(func() {
						ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
							ui.Spinner(c)
							ui.Text(c, snap.Status.Job.Message).FontSize(13).Bold().TextColor(pal.Text)
						})
						if sp := snap.Status.Job.SetupProgress; sp != nil {
							pct := float64(-1)
							if sp.Total > 0 && sp.Received != nil {
								pct = float64(*sp.Received) / float64(sp.Total)
							}
							ui.Progress(c, pct).FillWidth()
							if sp.Label != "" {
								info := sp.Label
								if sp.BytesPerSecond > 0 {
									info += fmt.Sprintf(" · %.1f MB/s", sp.BytesPerSecond/(1024*1024))
								}
								ui.Text(c, info).FontSize(11).TextColor(pal.Muted)
							}
						}
					})
				} else if snap.Status.Job.Kind != "proxy" && snap.Status.Job.Kind != "proxy-update" && snap.Status.Job.Error != "" {
					ErrorBanner(c, pal, snap.Status.Job.Error)
				}
				if snap.Status.Safety.Error != "" {
					ErrorBanner(c, pal, snap.Status.Safety.Error)
				}
				if !snap.Status.Ready && !snap.Status.Job.Running && snap.Proxy.Selected == "" {
					ui.Text(c, "提示：请先在第 2 步确认无冲突代理端口并选择一个真实出口节点（「设为出口」），再点击启动环境。").FontSize(12).TextColor(pal.Muted)
				}
				ui.Row(c).Gap(8).Wrap().Children(func() {
					if !snap.Status.Ready && !snap.Status.Job.Running {
						if snap.Proxy.Selected != "" {
							if ui.PrimaryButton(c, "安装 / 启动环境").Disabled(busy || snap.Status.Job.Running).Clicked() {
								go r.startOnboardingInstall()
							}
						} else {
							if ui.Button(c, "安装 / 启动环境").Disabled(busy || snap.Status.Job.Running).Clicked() {
								go r.startOnboardingInstall()
							}
						}
					}
					if opts != nil && opts.Activate {
						if ui.PrimaryButton(c, "激活已安装的客机").Disabled(busy || snap.Status.Job.Running).Clicked() {
							go r.activateInstalledGuest()
						}
					}
					if snap.Status.VMRunning && ui.Button(c, "停止环境").Disabled(busy || snap.Status.Job.Running).Clicked() {
						go r.postSimple("/api/stop", nil)
					}
					if ui.Button(c, "执行外网安全检查").Disabled(busy || snap.Status.Job.Running || !snap.Status.VMRunning).Clicked() {
						go r.runSafetyCheckOnboarding()
					}
					if ui.PrimaryButton(c, "完成初始化并进入工作台").Disabled(busy || snap.Status.Job.Running || !snap.Status.Ready).Clicked() {
						go r.completeInitialization()
					}
				})
			})
		})
	})
}

func safetyStatusLabel(snap core.Snapshot) string {
	g := snap.Status.Safety.Gate
	if g == nil {
		return "未检查"
	}
	switch g.Mode {
	case "open":
		if snap.Status.Safety.ExitIP != "" {
			return "已放行（出口 " + snap.Status.Safety.ExitIP + "）"
		}
		return "已放行"
	case "checking":
		return "正在检查"
	default:
		return "外网关闭"
	}
}

func permBadge(c *ui.Context, pal Palette, label string, ok bool) {
	if ok {
		PillBadge(c, "✓ "+label, pal.Success, pal.SuccessSoft)
	} else {
		PillBadge(c, "○ "+label, pal.Warning, pal.WarningSoft)
	}
}

func (r *RootView) startOnboardingInstall() {
	st := &r.onboarding
	st.mu.Lock()
	sys := st.selectedOS
	media := strings.TrimSpace(st.mediaURL)
	sha := strings.TrimSpace(st.mediaSHA256)
	terms := st.editorTerms
	st.busy = true
	st.errMsg = ""
	st.mu.Unlock()
	r.Invalidate()

	_ = r.Client.PostJSON(context.Background(), "/api/environment/initialization", map[string]string{"action": "install"}, nil)
	err := r.Client.PostJSON(context.Background(), "/api/start", map[string]any{
		"guest_system": sys,
		"media":        media,
		"sha256":       sha,
		"editor_terms": terms,
	}, nil)
	st.mu.Lock()
	st.busy = false
	if err != nil {
		st.errMsg = err.Error()
	} else {
		st.notice = "已开始安装/启动隔离客机环境"
	}
	st.mu.Unlock()
	r.Store.TriggerRefresh()
	r.Invalidate()
}

func (r *RootView) activateInstalledGuest() {
	st := &r.onboarding
	st.mu.Lock()
	st.busy = true
	st.errMsg = ""
	st.mu.Unlock()
	r.Invalidate()

	err := r.Client.PostJSON(context.Background(), "/api/environment/guest-systems/activate", map[string]any{}, nil)
	st.mu.Lock()
	st.busy = false
	if err != nil {
		st.errMsg = err.Error()
	}
	st.mu.Unlock()
	if err == nil {
		_ = r.Client.Bootstrap(context.Background())
	}
	r.Store.TriggerRefresh()
	r.Invalidate()
}

func (r *RootView) macFirstSetupAction(action string) {
	st := &r.onboarding
	st.mu.Lock()
	st.busy = true
	st.errMsg = ""
	st.mu.Unlock()
	r.Invalidate()

	var perm macSetupPermView
	err := r.Client.PostJSON(context.Background(), "/api/macos/first-setup", map[string]string{"action": action}, &perm)
	st.mu.Lock()
	st.busy = false
	if err != nil {
		st.errMsg = err.Error()
	} else if action == "permissions" || action == "verify-projects" || action == "confirm-sharing" || action == "check-sharing" {
		st.macPerms = &perm
	}
	st.mu.Unlock()
	r.Store.TriggerRefresh()
	r.Invalidate()
}

func onboardingProxySummary(px core.ProxyStatusView, port int) string {
	if px.Selected != "" {
		return fmt.Sprintf("%s（端口 %d）", proxySummaryLabel(px), port)
	}
	if len(px.Sources) > 0 {
		return fmt.Sprintf("已导入 %d 个订阅源 · 请在下方选择真实出口节点（目标端口 %d）", len(px.Sources), port)
	}
	return fmt.Sprintf("尚未配置（目标端口 %d）", port)
}

func proxySummaryLabel(px core.ProxyStatusView) string {
	label := strings.TrimSpace(px.StateLabel)
	if label == "" {
		label = "尚未配置"
	}
	if sel := selectedProxyNodeName(px); sel != "" {
		return label + " · " + sel
	}
	return label
}

func (r *RootView) runSafetyCheckOnboarding() {
	st := &r.onboarding
	st.mu.Lock()
	st.busy = true
	st.errMsg = ""
	st.notice = ""
	st.mu.Unlock()
	r.Invalidate()

	_, err := r.Store.RunTrackedJob(context.Background(), "/api/safety/check", map[string]any{})
	st.mu.Lock()
	st.busy = false
	if err != nil {
		st.errMsg = err.Error()
	} else {
		st.notice = "已完成外网安全检查"
	}
	st.mu.Unlock()
	r.Invalidate()
}

func (r *RootView) completeInitialization() {
	st := &r.onboarding
	st.mu.Lock()
	st.busy = true
	st.errMsg = ""
	st.mu.Unlock()
	r.Invalidate()

	err := r.Client.PostJSON(context.Background(), "/api/environment/initialization", map[string]string{"action": "complete"}, nil)
	st.mu.Lock()
	st.busy = false
	if err != nil {
		st.errMsg = err.Error()
	} else if st.setupSurface != nil {
		st.setupSurface.Stop()
		st.setupSurface = nil
	}
	st.mu.Unlock()
	r.Store.TriggerRefresh()
	r.Invalidate()
}

func (r *RootView) postSimple(path string, body any) {
	err := r.Client.PostJSON(context.Background(), path, body, nil)
	if err != nil {
		r.PushToast(err.Error())
	}
	r.Store.TriggerRefresh()
}
