package views

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/sylvinus/agent-vm/internal/mygoclient/core"
	"github.com/sylvinus/agent-vm/internal/mygoclient/surfaces"
)

type workbenchState struct {
	mu              sync.Mutex
	selectedCLIAcct string
	loginSurface    *surfaces.MacOSLoginSurface
	showLoginModal  bool
}

func (r *RootView) OpenMaintenanceModal(purpose string) {
	surf := surfaces.NewMacOSLoginSurface(r.Client, "/api/macos/maintenance", purpose, r.Invalidate)
	r.mu.Lock()
	if r.maintModal != nil && r.maintModal.surface != nil {
		r.maintModal.surface.Stop()
	}
	r.maintModal = &macOSMaintenanceModal{purpose: purpose, surface: surf}
	r.mu.Unlock()
	go func() { _ = surf.Open(context.Background()) }()
	r.Invalidate()
}

func (r *RootView) CloseMaintenanceModal() {
	r.mu.Lock()
	m := r.maintModal
	r.maintModal = nil
	r.mu.Unlock()
	if m != nil && m.surface != nil {
		m.surface.Stop()
	}
	r.Invalidate()
}

func (r *RootView) CreateTerminal(kind string) {
	r.CreateTerminalForWorkspace(kind, r.Store.Snapshot().Status.Workspace)
}

func (r *RootView) CreateTerminalForWorkspace(kind, targetWorkspace string) {
	snap := r.Store.Snapshot()
	r.workbench.mu.Lock()
	acctID := r.workbench.selectedCLIAcct
	r.workbench.mu.Unlock()
	go func() {
		ws := targetWorkspace
		if ws == "" {
			ws = snap.Status.Workspace
		}
		if kind != "claude-login" && ws == "" && snap.Status.GuestSystem != "windows-remote" {
			if len(snap.Status.Projects) > 0 {
				ws = snap.Status.Projects[0]
				_ = r.Client.PostJSON(context.Background(), "/api/projects/open", map[string]any{
					"workspace": ws,
					"add":       false,
					"activate":  true,
				}, nil)
			} else if r.Bridge != nil {
				picked, ok := r.pickAndActivateProject()
				if !ok {
					return
				}
				ws = picked
			}
		} else if ws != "" && ws != snap.Status.Workspace {
			_ = r.Client.PostJSON(context.Background(), "/api/projects/open", map[string]any{
				"workspace": ws,
				"add":       false,
				"activate":  true,
			}, nil)
		}
		var res core.TerminalSummary
		err := r.Client.PostJSON(context.Background(), "/api/terminals", map[string]string{
			"workspace":         ws,
			"kind":              kind,
			"claude_account_id": acctID,
		}, &res)
		if err != nil {
			r.PushToast("创建终端失败：" + err.Error())
			return
		}
		if res.ID != "" {
			r.Store.UpsertTerminal(res)
			r.mu.Lock()
			r.activeTermID = res.ID
			r.activeNav = NavTerminal
			r.mu.Unlock()
			if r.Invalidate != nil {
				r.Invalidate()
			}
		}
		r.Store.TriggerRefresh()
	}()
}

// CloseTerminal removes a terminal session on the controller and updates local view state.
func (r *RootView) CloseTerminal(id string) {
	if id == "" {
		return
	}
	go func() {
		if err := r.Client.DeleteJSON(context.Background(), "/api/terminals/"+id, nil); err != nil {
			r.PushToast("关闭终端失败：" + err.Error())
			r.Store.TriggerRefresh()
			return
		}
		r.mu.Lock()
		if r.activeTermID == id {
			r.activeTermID = ""
		}
		r.mu.Unlock()
		r.Store.RemoveTerminal(id)
		r.Store.TriggerRefresh()
	}()
}

func (r *RootView) renderWorkbench(c *ui.Context, pal Palette, snap core.Snapshot) {
	if c.Shortcut(ui.Cmd, ui.KeyBackslash) {
		r.mu.Lock()
		r.sidebarCollapsed = !r.sidebarCollapsed
		r.mu.Unlock()
	}
	if c.Shortcut(ui.Cmd, ui.KeyComma) {
		r.OpenSettings()
	}
	if snap.Status.Ready {
		if c.Shortcut(ui.Cmd, ui.KeyN) {
			r.CreateTerminal("claude")
		}
		if c.Shortcut(ui.Cmd, ui.KeyT) {
			r.CreateTerminal("shell")
		}
	}

	r.mu.Lock()
	sbWidth := r.sidebarWidth
	collapsed := r.sidebarCollapsed
	activeNav := r.activeNav
	r.mu.Unlock()

	if collapsed {
		r.renderWorkbenchContent(c, pal, snap, activeNav, true)
	} else {
		split := ui.Split(c, &sbWidth,
			func() { r.renderSidebar(c, pal, snap) },
			func() { r.renderWorkbenchContent(c, pal, snap, activeNav, false) },
		).SplitReset(252).Fill()

		if split.Changed() {
			r.mu.Lock()
			r.sidebarWidth = sbWidth
			r.mu.Unlock()
			r.Store.UpdateClientPreferences(func(p *core.ClientPreferences) {
				p.SidebarWidth = sbWidth
			})
		}
	}

	r.renderModals(c, pal, snap)
}

func (r *RootView) renderWorkbenchContent(c *ui.Context, pal Palette, snap core.Snapshot, activeNav string, sidebarCollapsed bool) {
	// Manage macOS guest frame WebSockets only when the browser view is visible.
	wantMacBrowser := snap.Status.Ready && snap.Status.VMRunning && snap.Status.GuestSystem == "macos" && activeNav == NavBrowser
	r.MacBrowser.EnsureRunning(context.Background(), wantMacBrowser)
	r.MacClaude.EnsureRunning(context.Background(), false)

	ui.Column(c).Fill().Background(pal.Bg).Children(func() {
		r.renderTopToolbar(c, pal, snap, activeNav, sidebarCollapsed)

		if snap.Status.Job.Running || snap.Status.Job.Error != "" || snap.Status.NetworkError != "" || snap.Status.ProjectError != "" {
			r.renderStatusAlerts(c, pal, snap)
		}

		ui.Box(c).FillWidth().Grow(1).Children(func() {
			switch activeNav {
			case NavTerminal, NavClaudeDesktop:
				r.renderTerminalWorkspace(c, pal, snap)
			case NavBrowser:
				r.renderBrowserView(c, pal, snap)
			case NavProxy:
				r.renderProxyView(c, pal, snap)
			case NavSafety:
				r.renderSafetyView(c, pal, snap)
			case NavClaudeAccounts:
				r.renderClaudeAccountsView(c, pal, snap)
			case NavTailscale:
				r.renderTailscaleView(c, pal, snap)
			case NavRemoteHosts:
				r.renderRemoteHostsView(c, pal, snap)
			case NavBrowserMgmt:
				r.renderBrowserMgmtView(c, pal, snap)
			case NavSettings:
				r.renderSettingsView(c, pal, snap)
			}
		})
	})
}

func navTitle(nav string) string {
	switch nav {
	case NavBrowser:
		return "隔离浏览器"
	case NavProxy:
		return "网络代理与节点（Mihomo）"
	case NavSafety:
		return "安全检查与出口"
	case NavClaudeAccounts:
		return "Claude 账号调度"
	case NavTailscale:
		return "Tailscale 私网"
	case NavRemoteHosts:
		return "远程 Cloud SSH"
	case NavBrowserMgmt:
		return "保险箱与文件管理"
	case NavSettings:
		return "通用、资源与诊断"
	default:
		return "项目工作台"
	}
}

func (r *RootView) activeTerminalSummary(snap core.Snapshot) (core.TerminalSummary, bool) {
	r.mu.Lock()
	activeID := r.activeTermID
	r.mu.Unlock()
	if activeID == "" {
		return core.TerminalSummary{}, false
	}
	for _, t := range snap.Terminals {
		if t.ID == activeID {
			return t, true
		}
	}
	return core.TerminalSummary{}, false
}

func (r *RootView) renderTopToolbar(c *ui.Context, pal Palette, snap core.Snapshot, activeNav string, sidebarCollapsed bool) {
	leftPad := float32(18)
	if sidebarCollapsed {
		bar := c.TitleBar()
		leftPad = 14
		if bar.Left > 0 {
			leftPad = bar.Left + 8
		}
	}

	activeTerm, hasActiveTerm := r.activeTerminalSummary(snap)
	projName := "未选择项目"
	if snap.Status.Workspace != "" {
		projName = filepath.Base(snap.Status.Workspace)
	}

	ui.Row(c).
		FillWidth().
		Height(44).
		Background(pal.Bg).
		BorderWidth(0, 0, 1, 0).
		BorderColor(pal.Border).
		Padding(0, 16, 0, leftPad).
		Justify(ui.SpaceBetween).
		AlignItems(ui.Center).
		DragWindow().
		Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Shrink(1).Children(func() {
				if sidebarCollapsed {
					if IconButton(c, pal, "tb_expand_sb", IconSidebarToggle, "展开侧边栏 (⌘\\)", false) {
						r.mu.Lock()
						r.sidebarCollapsed = false
						r.mu.Unlock()
					}
				}

				switch {
				case IsSettingsNav(activeNav):
					ui.Text(c, "设置").FontSize(13).TextColor(pal.Muted).SingleLine()
					ui.Text(c, "/").FontSize(13).TextColor(pal.SubtleText).SingleLine()
					ui.Text(c, navTitle(activeNav)).FontSize(13).Bold().TextColor(pal.Text).SingleLine()
				case activeNav == NavBrowser:
					ui.Text(c, "使用").FontSize(13).TextColor(pal.Muted).SingleLine()
					ui.Text(c, "/").FontSize(13).TextColor(pal.SubtleText).SingleLine()
					ui.Text(c, "隔离浏览器").FontSize(13).Bold().TextColor(pal.Text).SingleLine()
				case hasActiveTerm && snap.Status.Ready:
					lbl := activeTerm.Title
					if activeTerm.Kind == "claude" && lbl == "" {
						lbl = "Claude 终端"
					} else if lbl == "" {
						lbl = "CLI"
					}
					ui.Text(c, projName).FontSize(13).TextColor(pal.Muted).SingleLine()
					ui.Text(c, "/").FontSize(13).TextColor(pal.SubtleText).SingleLine()
					ui.Text(c, lbl).FontSize(13).Bold().TextColor(pal.Text).SingleLine().Shrink(1)
				default:
					ui.Text(c, "使用").FontSize(13).TextColor(pal.Muted).SingleLine()
					ui.Text(c, "/").FontSize(13).TextColor(pal.SubtleText).SingleLine()
					ui.Text(c, projName).FontSize(13).Bold().TextColor(pal.Text).SingleLine()
				}

				// Subtle inline environment & gate status pill
				dotColor := pal.SubtleText
				pillText := "已停止"
				if snap.Status.Ready && snap.Status.VMRunning {
					pillText = safetyStatusLabel(snap)
					if snap.Status.Safety.Gate != nil && snap.Status.Safety.Gate.Mode == "open" {
						dotColor = pal.Success
					} else {
						dotColor = pal.Warning
					}
				} else if snap.Status.Job.Running {
					dotColor = pal.Warning
					pillText = "处理中"
				}
				ui.Row(c).Background(pal.ElevatedBg).Radius(999).Padding(2, 9).Gap(5).AlignItems(ui.Center).Children(func() {
					StatusDot(c, dotColor, 6)
					ui.Text(c, pillText).FontSize(11).TextColor(pal.Muted).SingleLine()
				})

				if snap.Status.EnvironmentUpdateRequired {
					if CompactChipButton(c, pal, "tb_env_update", nil, "组件需更新", false, false) {
						r.SelectNav(NavSettings)
					}
				}
			})

			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				if IsSettingsNav(activeNav) {
					if CompactChipButton(c, pal, "tb_settings_done", nil, "完成", false, false) {
						r.CloseSettings()
					}
					return
				}

				if activeNav == NavTerminal && hasActiveTerm && snap.Status.Ready {
					if IconButton(c, pal, "tb_upload_file", IconUpload, "传入文件到当前终端", false) {
						go r.uploadFilesToTerminal(activeTerm.ID)
					}
					if CompactChipButton(c, pal, "tb_back_proj", IconFolder, "项目主页", false, false) {
						r.mu.Lock()
						r.activeTermID = ""
						r.mu.Unlock()
					}
				}

				if snap.Status.GuestSystem == "macos" && snap.Status.VMRunning {
					if CompactChipButton(c, pal, "tb_mac_login", nil, "客机登录画面", false, false) {
						r.openMacLoginModal()
					}
				}
				if snap.Status.Job.Log != "" {
					lbl := "查看日志"
					r.mu.Lock()
					if r.showJobLog {
						lbl = "隐藏日志"
					}
					r.mu.Unlock()
					if CompactChipButton(c, pal, "tb_toggle_log", IconLog, lbl, false, false) {
						r.mu.Lock()
						r.showJobLog = !r.showJobLog
						r.mu.Unlock()
					}
				}
				if IconButton(c, pal, "tb_refresh", IconRefresh, "刷新状态", false) {
					r.Store.TriggerRefresh()
				}
			})
		})
}

func (r *RootView) renderStatusAlerts(c *ui.Context, pal Palette, snap core.Snapshot) {
	ui.Column(c).FillWidth().Padding(8, 14).Gap(6).Children(func() {
		if snap.Status.Job.Running {
			ui.Row(c).Background(pal.AccentSoft).Border(1, pal.Accent).Radius(8).Padding(8, 12).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Spinner(c)
				ui.Text(c, snap.Status.Job.Message).FontSize(12).Bold().TextColor(pal.Accent).Grow(1)
				if sp := snap.Status.Job.SetupProgress; sp != nil && sp.Total > 0 && sp.Received != nil {
					ui.Progress(c, float64(*sp.Received)/float64(sp.Total)).Width(140)
				}
			})
		}
		r.renderActionableErrorBanner(c, pal, snap, "job_err", snap.Status.Job.Error)
		r.renderActionableErrorBanner(c, pal, snap, "net_err", snap.Status.NetworkError)
		ErrorBanner(c, pal, snap.Status.ProjectError)
		r.mu.Lock()
		showLog := r.showJobLog
		r.mu.Unlock()
		if showLog && snap.Status.Job.Log != "" {
			ui.Scroll(c).MaxHeight(160).FillWidth().Background(pal.CardBg).Border(1, pal.Border).Radius(8).Padding(10).Children(func() {
				ui.Text(c, snap.Status.Job.Log).FontSize(11).TextColor(pal.Text).Selectable()
			})
		}
	})
}

func (r *RootView) renderActionableErrorBanner(c *ui.Context, pal Palette, snap core.Snapshot, id, msg string) {
	if msg == "" {
		return
	}
	if strings.Contains(msg, "需要重启环境") {
		ui.Row(c).
			FillWidth().
			Background(pal.DangerSoft).
			Border(1, pal.Danger).
			Radius(8).
			Padding(8, 12).
			Gap(10).
			AlignItems(ui.Center).
			Children(func() {
				ui.Text(c, msg).FontSize(12).TextColor(pal.Danger).Grow(1).Selectable()
				if CompactChipButton(c, pal, "restart_env_"+id, IconRefresh, "立即重启环境", true, snap.Status.Job.Running) {
					go r.postSimple("/api/proxy/restart", map[string]any{})
				}
			})
		return
	}
	ErrorBanner(c, pal, msg)
}

func (r *RootView) renderTerminalWorkspace(c *ui.Context, pal Palette, snap core.Snapshot) {
	activeTerm, hasActiveTerm := r.activeTerminalSummary(snap)
	if !hasActiveTerm {
		r.mu.Lock()
		if r.activeTermID != "" {
			r.activeTermID = ""
		}
		r.mu.Unlock()
	}

	ui.Column(c).Fill().Background(pal.Bg).Gap(0).Children(func() {
		if !snap.Status.Ready || !hasActiveTerm {
			r.renderProjectHome(c, pal, snap)
			return
		}

		// Active Ghostty Terminal View
		ui.Column(c).FillWidth().Grow(1).Gap(0).Children(func() {
			sess, err := r.Terminals.GetOrOpen(context.Background(), activeTerm.ID)
			if err != nil {
				ui.Box(c).FillWidth().Padding(10, 12).Children(func() {
					ErrorBanner(c, pal, "终端连接失败："+err.Error())
				})
				return
			}
			if sess == nil {
				return
			}
			cliStarting := sess.CLIStarting()
			loginURL := sess.DetectedLoginURL()
			lerr := sess.LastError()
			if cliStarting || loginURL != "" || activeTerm.Closed || lerr != "" {
				ui.Column(c).FillWidth().Padding(8, 12).Gap(8).Children(func() {
					if cliStarting {
						ui.Row(c).Background(pal.AccentSoft).Border(1, pal.Accent).Radius(6).Padding(6, 10).Gap(8).AlignItems(ui.Center).Children(func() {
							ui.Spinner(c)
							ui.Text(c, "Claude CLI 正在启动并载入安全沙盒…").FontSize(12).TextColor(pal.Accent)
						})
					}
					if loginURL != "" {
						ui.Row(c).Background(pal.WarningSoft).Border(1, pal.Warning).Radius(6).Padding(8, 12).Gap(10).AlignItems(ui.Center).Children(func() {
							ui.Text(c, "检测到 Claude 官方登录授权链接，建议在隔离浏览器中打开完成授权").FontSize(12).TextColor(pal.Text).Grow(1)
							if ui.PrimaryButton(c, "在隔离浏览器打开").Clicked() {
								go func(u string) {
									if err := sess.OpenLoginURL(context.Background(), u); err == nil {
										r.SelectNav(NavBrowser)
										r.Store.TriggerRefresh()
									}
								}(loginURL)
							}
							if ui.Button(c, "忽略").Clicked() {
								sess.DismissLoginURL()
							}
						})
					}
					if activeTerm.Closed {
						ui.Row(c).Background(pal.ElevatedBg).Border(1, pal.Border).Radius(6).Padding(8, 12).Gap(10).AlignItems(ui.Center).Children(func() {
							ui.Text(c, "终端会话已结束，可查看输出历史或关闭以释放会话额度").FontSize(12).TextColor(pal.Muted).Grow(1)
							if CompactChipButton(c, pal, "term_close_ended_"+activeTerm.ID, IconClose, "关闭终端", false, false) {
								r.CloseTerminal(activeTerm.ID)
							}
						})
					}
					if lerr != "" {
						ErrorBanner(c, pal, lerr)
					}
				})
			}
			ui.Box(c).FillWidth().Grow(1).Children(func() {
				sess.View(c)
			})
		})
	})
}

func (r *RootView) renderProjectHome(c *ui.Context, pal Palette, snap core.Snapshot) {
	ws := snap.Status.Workspace
	projName := "未选择项目"
	pathLabel := "尚未选择项目目录（点击右侧添加或授权宿主机项目）"
	if ws != "" {
		projName = filepath.Base(ws)
		pathLabel = ws
	}

	guestLabel := "Linux"
	if snap.Status.GuestSystem != "" {
		guestLabel = snap.Status.GuestSystem
	}
	connName := "本机 Cage"
	if snap.Context.Mode == "remote" {
		if snap.Context.Name != "" {
			connName = snap.Context.Name
		} else {
			connName = "远程主机"
		}
	}
	vmRunning := snap.Status.Ready && snap.Status.VMRunning
	safetyText := safetyStatusLabel(snap)
	safetyDot := pal.Warning
	if snap.Status.Safety.Gate != nil && snap.Status.Safety.Gate.Mode == "open" {
		safetyDot = pal.Success
	}

	r.workbench.mu.Lock()
	selAcct := r.workbench.selectedCLIAcct
	r.workbench.mu.Unlock()

	var projectTerms []core.TerminalSummary
	openTermIDs := map[string]bool{}
	for _, t := range snap.Terminals {
		openTermIDs[t.ID] = true
		if ws == "" || t.Workspace == ws || t.Workspace == "" {
			projectTerms = append(projectTerms, t)
		}
	}
	var projectSessions []core.CLISessionSummary
	for _, cs := range snap.CLISessions {
		if cs.Archived || (cs.TerminalID != "" && openTermIDs[cs.TerminalID]) {
			continue
		}
		if ws == "" || cs.Workspace == ws {
			projectSessions = append(projectSessions, cs)
		}
	}

	// Top-aligned scrollable layout modeled directly after Magpie's clean native page structure.
	ui.Scroll(c).FillWidth().Grow(1).Padding(18, 24, 24, 24).Children(func() {
		ui.Column(c).FillWidth().Gap(14).Children(func() {
			// 1. Page description & status summary bar (Magpie top header style)
			ui.Column(c).FillWidth().Gap(8).Children(func() {
				ui.Text(c, "在安全隔离的 Cage 客机沙盒中运行 Claude Code、命令行终端与独立指纹浏览器；宿主机未授权目录对客机完全不可见。").
					FontSize(12).
					TextColor(pal.Muted)

				ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Gap(12).Children(func() {
					ui.Row(c).Gap(7).AlignItems(ui.Center).Shrink(1).Children(func() {
						if vmRunning {
							StatusDot(c, pal.Success, 7)
							ui.Text(c, fmt.Sprintf("%s（%s）运行中 · %s · 当前工作区：%s", connName, guestLabel, safetyText, projName)).
								FontSize(12).
								TextColor(pal.Text).
								SingleLine().
								Shrink(1)
						} else if snap.Status.Job.Running {
							StatusDot(c, pal.Warning, 7)
							msg := snap.Status.Job.Message
							if msg == "" {
								msg = "正在处理环境操作…"
							}
							ui.Text(c, fmt.Sprintf("%s（%s）· %s", connName, guestLabel, msg)).
								FontSize(12).
								TextColor(pal.Text).
								SingleLine().
								Shrink(1)
						} else {
							StatusDot(c, pal.SubtleText, 7)
							ui.Text(c, fmt.Sprintf("%s（%s）当前尚未启动 · 启动环境后即可在「%s」开启隔离终端", connName, guestLabel, projName)).
								FontSize(12).
								TextColor(pal.Text).
								SingleLine().
								Shrink(1)
						}
					})

					ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
						if snap.Status.CanRestore {
							if CompactChipButton(c, pal, "home_restore_env", nil, "恢复备份", false, snap.Status.Job.Running) {
								go r.postSimple("/api/restore", nil)
							}
						}
						if !snap.Status.VMRunning {
							if CompactChipButton(c, pal, "home_start_env", IconPlay, "启动环境", true, snap.Status.Job.Running) {
								go r.postSimple("/api/start", map[string]any{})
							}
						} else {
							if CompactChipButton(c, pal, "home_pick_proj_top", IconFolderPlus, "添加项目", false, false) {
								go r.pickAndOpenProject()
							}
						}
					})
				})
			})

			// 2. Primary Magpie-style Grouped Divided List Card
			ui.Column(c).
				FillWidth().
				Background(pal.CardBg).
				Border(1, pal.Border).
				Radius(10).
				Children(func() {
					// Row 1: Claude Code Agent CLI
					claudeCmd := snap.Settings.ClaudeTerminalCommand
					if claudeCmd == "" {
						claudeCmd = "claude"
					}
					ui.Row(c).
						FillWidth().
						Padding(14, 18).
						Justify(ui.SpaceBetween).
						AlignItems(ui.Center).
						Gap(14).
						Children(func() {
							ui.Row(c).Gap(13).AlignItems(ui.Center).Grow(1).Shrink(1).Children(func() {
								ui.Box(c).Size(34, 34).Radius(8).Background(pal.AccentSoft).Center().Children(func() {
									ui.Icon(c, IconSparkles).FontSize(16).TextColor(pal.Accent)
								})
								ui.Column(c).Gap(3).Grow(1).Shrink(1).Children(func() {
									ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
										ui.Text(c, "Claude Code").FontSize(13).Bold().TextColor(pal.Text).SingleLine()
										ui.Text(c, claudeCmd+" · ⌘N").FontSize(11).TextColor(pal.SubtleText).SingleLine()
									})
									ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
										if vmRunning {
											StatusDot(c, pal.Success, 6)
											ui.Text(c, "已就绪 · 在隔离沙盒中启动 Claude Code 并自动挂载 "+projName).
												FontSize(11).
												TextColor(pal.Muted).
												SingleLine().
												Shrink(1)
										} else {
											StatusDot(c, pal.Warning, 6)
											ui.Text(c, "待启动 · 启动客机环境后在 "+projName+" 目录内运行 Claude Code").
												FontSize(11).
												TextColor(pal.Warning).
												SingleLine().
												Shrink(1)
										}
									})
								})
							})

							ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
								if len(snap.ClaudePanel.Accounts) > 0 {
									acctLabel := "自动调度账号"
									for _, a := range snap.ClaudePanel.Accounts {
										if a.ID == selAcct {
											acctLabel = a.Label
										}
									}
									ui.MenuButton(c, acctLabel, func(m *ui.Menu) {
										if m.Item("自动调度 / 默认账号").Checked(selAcct == "").Chosen() {
											r.workbench.mu.Lock()
											r.workbench.selectedCLIAcct = ""
											r.workbench.mu.Unlock()
										}
										for _, a := range snap.ClaudePanel.Accounts {
											acct := a
											if m.Item(acct.Label).Checked(selAcct == acct.ID).Chosen() {
												r.workbench.mu.Lock()
												r.workbench.selectedCLIAcct = acct.ID
												r.workbench.mu.Unlock()
											}
										}
									})
								}
								if CompactChipButton(c, pal, "home_launch_claude", IconSparkles, "新建 Claude 终端", true, snap.Status.Job.Running) {
									if !snap.Status.Ready {
										go r.postSimple("/api/start", map[string]any{})
									} else {
										r.CreateTerminal("claude")
									}
								}
							})
						})

					// Row 2: System Shell / CLI Terminal
					ui.Row(c).
						FillWidth().
						BorderWidth(1, 0, 0, 0).
						BorderColor(pal.Border).
						Padding(14, 18).
						Justify(ui.SpaceBetween).
						AlignItems(ui.Center).
						Gap(14).
						Children(func() {
							ui.Row(c).Gap(13).AlignItems(ui.Center).Grow(1).Shrink(1).Children(func() {
								ui.Box(c).Size(34, 34).Radius(8).Background(pal.ElevatedBg).Center().Children(func() {
									ui.Icon(c, IconTerminal).FontSize(16).TextColor(pal.Text)
								})
								ui.Column(c).Gap(3).Grow(1).Shrink(1).Children(func() {
									ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
										ui.Text(c, "系统命令行终端").FontSize(13).Bold().TextColor(pal.Text).SingleLine()
										ui.Text(c, guestLabel+" · Bash / Zsh · ⌘T").FontSize(11).TextColor(pal.SubtleText).SingleLine()
									})
									ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
										if vmRunning {
											StatusDot(c, pal.Success, 6)
											ui.Text(c, "已就绪 · 打开客机交互式终端，默认进入项目目录 "+projName).
												FontSize(11).
												TextColor(pal.Muted).
												SingleLine().
												Shrink(1)
										} else {
											StatusDot(c, pal.SubtleText, 6)
											ui.Text(c, "待启动 · 启动环境后可使用完整 Linux / macOS 客机命令行").
												FontSize(11).
												TextColor(pal.Muted).
												SingleLine().
												Shrink(1)
										}
									})
								})
							})

							ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
								ui.Box(c).Background(pal.ElevatedBg).Radius(6).Padding(4, 10).Children(func() {
									ui.Text(c, projName).FontSize(11).TextColor(pal.Muted).SingleLine()
								})
								if CompactChipButton(c, pal, "home_launch_cli", IconTerminal, "新建 CLI", false, snap.Status.Job.Running) {
									if !snap.Status.Ready {
										go r.postSimple("/api/start", map[string]any{})
									} else {
										r.CreateTerminal("shell")
									}
								}
							})
						})

					// Row 3: Isolated Browser
					tabCount := len(snap.BrowserTabs.Tabs)
					ui.Row(c).
						FillWidth().
						BorderWidth(1, 0, 0, 0).
						BorderColor(pal.Border).
						Padding(14, 18).
						Justify(ui.SpaceBetween).
						AlignItems(ui.Center).
						Gap(14).
						Children(func() {
							ui.Row(c).Gap(13).AlignItems(ui.Center).Grow(1).Shrink(1).Children(func() {
								ui.Box(c).Size(34, 34).Radius(8).Background(pal.ElevatedBg).Center().Children(func() {
									ui.Icon(c, IconBrowser).FontSize(16).TextColor(pal.Text)
								})
								ui.Column(c).Gap(3).Grow(1).Shrink(1).Children(func() {
									ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
										ui.Text(c, "隔离浏览器").FontSize(13).Bold().TextColor(pal.Text).SingleLine()
										ui.Text(c, "Chromium · 独立指纹沙盒").FontSize(11).TextColor(pal.SubtleText).SingleLine()
									})
									ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
										if vmRunning {
											StatusDot(c, pal.Success, 6)
										} else {
											StatusDot(c, pal.SubtleText, 6)
										}
										ui.Text(c, "客机内运行独立浏览器，支持 Claude 官方登录授权、密码保险箱填充与文件安全中转").
											FontSize(11).
											TextColor(pal.Muted).
											SingleLine().
											Shrink(1)
									})
								})
							})

							ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
								ui.Box(c).Background(pal.ElevatedBg).Radius(6).Padding(4, 10).Children(func() {
									ui.Text(c, fmt.Sprintf("%d 个标签页", tabCount)).FontSize(11).TextColor(pal.Muted).SingleLine()
								})
								if CompactChipButton(c, pal, "home_open_browser", nil, "打开浏览器 ›", false, false) {
									r.SelectNav(NavBrowser)
								}
							})
						})

					// Row 4: Network Proxy & Safety Gate
					proxyState := snap.Proxy.StateLabel
					if proxyState == "" {
						proxyState = "内置 Mihomo 核心"
					}
					ui.Row(c).
						FillWidth().
						BorderWidth(1, 0, 0, 0).
						BorderColor(pal.Border).
						Padding(14, 18).
						Justify(ui.SpaceBetween).
						AlignItems(ui.Center).
						Gap(14).
						Children(func() {
							ui.Row(c).Gap(13).AlignItems(ui.Center).Grow(1).Shrink(1).Children(func() {
								ui.Box(c).Size(34, 34).Radius(8).Background(pal.ElevatedBg).Center().Children(func() {
									ui.Icon(c, IconShield).FontSize(16).TextColor(safetyDot)
								})
								ui.Column(c).Gap(3).Grow(1).Shrink(1).Children(func() {
									ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
										ui.Text(c, "网络代理与出口门控").FontSize(13).Bold().TextColor(pal.Text).SingleLine()
										ui.Text(c, proxyState).FontSize(11).TextColor(pal.SubtleText).SingleLine()
									})
									ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
										StatusDot(c, safetyDot, 6)
										ui.Text(c, safetyText+" · 客机公网 UDP / IPv6 默认阻断，异常断网自动关闭外网防直连泄漏").
											FontSize(11).
											TextColor(pal.Muted).
											SingleLine().
											Shrink(1)
									})
								})
							})

							ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
								if CompactChipButton(c, pal, "home_open_safety", nil, "安全检查 ›", false, false) {
									r.SelectNav(NavSafety)
								}
								if CompactChipButton(c, pal, "home_open_proxy", nil, "节点设置 ›", false, false) {
									r.SelectNav(NavProxy)
								}
							})
						})
				})

			// 3. Active & Historical Sessions Section (when sessions exist)
			totalSessions := len(projectTerms) + len(projectSessions)
			if totalSessions > 0 {
				ui.Column(c).FillWidth().Gap(6).Children(func() {
					ui.Text(c, fmt.Sprintf("当前项目会话 (%d)", totalSessions)).
						FontSize(12).
						Bold().
						TextColor(pal.Muted)

					ui.Column(c).
						FillWidth().
						Background(pal.CardBg).
						Border(1, pal.Border).
						Radius(10).
						Children(func() {
							rowIdx := 0
							for _, t := range projectTerms {
								term := t
								lbl := term.Title
								if lbl == "" {
									if term.Kind == "claude" {
										lbl = "Claude 终端"
									} else {
										lbl = "CLI · " + projName
									}
								}
								rRow := ui.Row(c.Key("home_term_" + term.ID)).
									FillWidth().
									Padding(11, 16).
									Justify(ui.SpaceBetween).
									AlignItems(ui.Center).
									Gap(12)
								if rowIdx > 0 {
									rRow.BorderWidth(1, 0, 0, 0).BorderColor(pal.Border)
								}
								rowIdx++
								rRow.Children(func() {
									dotColor := pal.Success
									stateText := "运行中"
									stateColor := pal.Success
									enterText := "进入终端 ›"
									if term.Closed {
										dotColor = pal.SubtleText
										stateText = "已结束"
										stateColor = pal.SubtleText
										enterText = "查看输出 ›"
									}
									ui.Row(c).Gap(10).AlignItems(ui.Center).Grow(1).Shrink(1).Children(func() {
										StatusDot(c, dotColor, 6)
										ui.Text(c, lbl).FontSize(12).Bold().TextColor(pal.Text).SingleLine().Shrink(1)
										ui.Text(c, stateText).FontSize(11).TextColor(stateColor).SingleLine()
									})
									ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
										if CompactChipButton(c, pal, "home_close_term_"+term.ID, IconClose, "关闭", false, false) {
											r.CloseTerminal(term.ID)
										}
										if CompactChipButton(c, pal, "home_enter_term_"+term.ID, nil, enterText, false, false) {
											r.mu.Lock()
											r.activeTermID = term.ID
											r.activeNav = NavTerminal
											r.mu.Unlock()
										}
									})
								})
							}

							for _, cs := range projectSessions {
								sess := cs
								lbl := sess.Title
								if lbl == "" {
									lbl = sess.ID
								}
								rRow := ui.Row(c.Key("home_sess_" + sess.ID)).
									FillWidth().
									Padding(11, 16).
									Justify(ui.SpaceBetween).
									AlignItems(ui.Center).
									Gap(12)
								if rowIdx > 0 {
									rRow.BorderWidth(1, 0, 0, 0).BorderColor(pal.Border)
								}
								rowIdx++
								rRow.Children(func() {
									ui.Row(c).Gap(10).AlignItems(ui.Center).Grow(1).Shrink(1).Children(func() {
										StatusDot(c, pal.SubtleText, 6)
										ui.Text(c, lbl).FontSize(12).TextColor(pal.Text).SingleLine().Shrink(1)
										ui.Text(c, "历史 CLI 会话").FontSize(11).TextColor(pal.SubtleText).SingleLine()
									})
									if CompactChipButton(c, pal, "home_open_sess_"+sess.ID, nil, "恢复会话 ›", false, !snap.Status.Ready) {
										id := sess.ID
										go func() {
											if err := r.Client.PostJSON(context.Background(), "/api/cli/sessions/"+id+"/open", map[string]any{}, nil); err != nil {
												r.PushToast(err.Error())
											}
											r.Store.TriggerRefresh()
										}()
									}
								})
							}
						})
				})
			}

			// 4. Secondary Magpie-style Banner Strip: Host Project Mount Info
			mountCard := ui.ButtonBase(c.Key("home_proj_path")).
				FillWidth().
				Background(pal.CardBg).
				Border(1, pal.Border).
				Radius(9).
				Padding(11, 16).
				Justify(ui.SpaceBetween).
				AlignItems(ui.Center).
				Gap(12).
				Label(pathLabel)
			if mountCard.Hovered() {
				mountCard.Background(pal.ElevatedBg)
			}
			mountCard.Children(func() {
				ui.Row(c).Gap(10).AlignItems(ui.Center).Grow(1).Shrink(1).Children(func() {
					ui.Icon(c, IconFolder).FontSize(14).TextColor(pal.Accent)
					ui.Text(c, "项目目录隔离").FontSize(12).Bold().TextColor(pal.Accent).SingleLine()
					ui.Text(c, pathLabel).FontSize(12).TextColor(pal.Muted).SingleLine().Shrink(1)
				})
				ui.Text(c, "切换或添加目录 ›").FontSize(11).TextColor(pal.Muted).SingleLine()
			})
			if mountCard.Clicked() {
				go r.pickAndOpenProject()
			}
		})
	})
}

func (r *RootView) uploadFilesToTerminal(termID string) {
	if r.Bridge == nil {
		return
	}
	paths, err := r.Bridge.OpenDialog(core.NativeDialogOptions{
		Title:    "选择要传入当前终端的文件",
		Multiple: true,
	})
	if err != nil || len(paths) == 0 {
		return
	}
	args := append([]string{"terminal", termID}, paths...)
	res, err := r.Client.RunFileCommand(context.Background(), "upload-files", args...)
	if err != nil {
		r.PushToast("上传文件失败：" + err.Error())
		return
	}
	r.PushToast("文件已上传到隔离环境附件目录")
	_ = res
}

func (r *RootView) openMacLoginModal() {
	surf := surfaces.NewMacOSLoginSurface(r.Client, "/api/macos/login", "", r.Invalidate)
	r.workbench.mu.Lock()
	if r.workbench.loginSurface != nil {
		r.workbench.loginSurface.Stop()
	}
	r.workbench.loginSurface = surf
	r.workbench.showLoginModal = true
	r.workbench.mu.Unlock()
	go func() { _ = surf.Open(context.Background()) }()
	r.Invalidate()
}

func (r *RootView) renderModals(c *ui.Context, pal Palette, _ core.Snapshot) {
	r.workbench.mu.Lock()
	showLogin := r.workbench.showLoginModal
	loginSurf := r.workbench.loginSurface
	r.workbench.mu.Unlock()

	if showLogin {
		if ui.Modal(c, &showLogin, func() {
			ui.Column(c).Width(760).Background(pal.CardBg).Border(1, pal.Border).Radius(12).Padding(16).Gap(10).Children(func() {
				ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "macOS 客机登录控制台").FontSize(15).Bold().TextColor(pal.Text)
					ui.Row(c).Gap(8).Children(func() {
						if loginSurf != nil {
							if ui.Button(c, "填入密码").Clicked() {
								go func() { _ = loginSurf.FillPassword(context.Background()) }()
							}
						}
						if ui.Button(c, "关闭").Clicked() {
							r.workbench.mu.Lock()
							r.workbench.showLoginModal = false
							if r.workbench.loginSurface != nil {
								r.workbench.loginSurface.Stop()
								r.workbench.loginSurface = nil
							}
							r.workbench.mu.Unlock()
						}
					})
				})
				if loginSurf != nil {
					ui.Box(c).FillWidth().Height(460).Radius(8).Border(1, pal.Border).Children(func() {
						loginSurf.View(c)
					})
				}
			})
		}).Dismissed() {
			r.workbench.mu.Lock()
			r.workbench.showLoginModal = false
			if r.workbench.loginSurface != nil {
				r.workbench.loginSurface.Stop()
				r.workbench.loginSurface = nil
			}
			r.workbench.mu.Unlock()
		}
	}

	r.mu.Lock()
	mm := r.maintModal
	r.mu.Unlock()
	if mm != nil {
		open := true
		if ui.Modal(c, &open, func() {
			ui.Column(c).Width(780).Background(pal.CardBg).Border(1, pal.Border).Radius(12).Padding(16).Gap(10).Children(func() {
				ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
					ui.Text(c, "macOS 客机系统维护："+mm.purpose).FontSize(15).Bold().TextColor(pal.Text)
					ui.Row(c).Gap(8).Children(func() {
						if mm.surface != nil {
							if ui.Button(c, "填入密码").Clicked() {
								go func() { _ = mm.surface.FillPassword(context.Background()) }()
							}
						}
						if ui.Button(c, "完成并关闭").Clicked() {
							r.CloseMaintenanceModal()
						}
					})
				})
				if mm.surface != nil {
					ui.Box(c).FillWidth().Height(480).Radius(8).Border(1, pal.Border).Children(func() {
						mm.surface.View(c)
					})
				}
			})
		}).Dismissed() {
			r.CloseMaintenanceModal()
		}
	}
}
