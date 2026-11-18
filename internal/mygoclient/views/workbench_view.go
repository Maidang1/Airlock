package views

import (
	"context"
	"path/filepath"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/sylvinus/agent-vm/internal/mygoclient/core"
	"github.com/sylvinus/agent-vm/internal/mygoclient/surfaces"
)

type workbenchState struct {
	mu               sync.Mutex
	selectedCLIAcct  string
	showCLISessions  bool
	loginSurface     *surfaces.MacOSLoginSurface
	showLoginModal   bool
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
	snap := r.Store.Snapshot()
	r.workbench.mu.Lock()
	acctID := r.workbench.selectedCLIAcct
	r.workbench.mu.Unlock()
	go func() {
		ws := snap.Status.Workspace
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
		}
		var res struct {
			ID string `json:"id"`
		}
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
			r.mu.Lock()
			r.activeTermID = res.ID
			r.activeNav = NavTerminal
			r.mu.Unlock()
		}
		r.Store.TriggerRefresh()
	}()
}

func (r *RootView) renderWorkbench(c *ui.Context, pal Palette, snap core.Snapshot) {
	if c.Shortcut(ui.Cmd, ui.KeyBackslash) {
		r.mu.Lock()
		r.sidebarCollapsed = !r.sidebarCollapsed
		r.mu.Unlock()
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
		).SplitReset(218).Fill()

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
	// Manage macOS guest frame WebSockets only when the corresponding view is visible.
	wantMacBrowser := snap.Status.Ready && snap.Status.VMRunning && snap.Status.GuestSystem == "macos" && activeNav == NavBrowser
	wantMacClaude := snap.Status.Ready && snap.Status.VMRunning && snap.Status.GuestSystem == "macos" && activeNav == NavClaudeDesktop && snap.ClaudeDesktop.Running
	r.MacBrowser.EnsureRunning(context.Background(), wantMacBrowser)
	r.MacClaude.EnsureRunning(context.Background(), wantMacClaude)

	ui.Column(c).Fill().Background(pal.Bg).Children(func() {
		r.renderTopToolbar(c, pal, snap, activeNav, sidebarCollapsed)

		if snap.Status.Job.Running || snap.Status.Job.Error != "" || snap.Status.NetworkError != "" || snap.Status.ProjectError != "" {
			r.renderStatusAlerts(c, pal, snap)
		}

		ui.Box(c).FillWidth().Grow(1).Children(func() {
			switch activeNav {
			case NavTerminal:
				r.renderTerminalWorkspace(c, pal, snap)
			case NavBrowser:
				r.renderBrowserView(c, pal, snap)
			case NavClaudeDesktop:
				r.renderClaudeDesktopView(c, pal, snap)
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

func navCategory(nav string) string {
	switch nav {
	case NavTerminal, NavBrowser, NavClaudeDesktop:
		return "工作区"
	case NavProxy, NavSafety, NavTailscale, NavRemoteHosts:
		return "网络与环境"
	case NavClaudeAccounts, NavBrowserMgmt, NavSettings:
		return "管理与设置"
	default:
		return "Cage"
	}
}

func navTitle(nav string) string {
	switch nav {
	case NavTerminal:
		return "终端与 Claude Code"
	case NavBrowser:
		return "隔离浏览器"
	case NavClaudeDesktop:
		return "Claude Desktop"
	case NavProxy:
		return "网络代理与节点调度"
	case NavSafety:
		return "外网安全门控与检查"
	case NavClaudeAccounts:
		return "Claude 多账号与额度调度"
	case NavTailscale:
		return "Tailscale 私网访问"
	case NavRemoteHosts:
		return "远程 Cloud SSH 主机"
	case NavBrowserMgmt:
		return "密码保险箱与文件管理"
	case NavSettings:
		return "设置、存储与诊断"
	default:
		return "Cage 工作台"
	}
}

func (r *RootView) renderTopToolbar(c *ui.Context, pal Palette, snap core.Snapshot, activeNav string, sidebarCollapsed bool) {
	leftPad := float32(16)
	if sidebarCollapsed {
		bar := c.TitleBar()
		leftPad = 14
		if bar.Left > 0 {
			leftPad = bar.Left + 8
		}
	}

	ui.Row(c).
		FillWidth().
		Height(48).
		Background(pal.Bg).
		BorderWidth(0, 0, 1, 0).
		BorderColor(pal.Border).
		Padding(0, 16, 0, leftPad).
		Justify(ui.SpaceBetween).
		AlignItems(ui.Center).
		DragWindow().
		Children(func() {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if sidebarCollapsed {
					if IconButton(c, pal, "tb_expand_sb", IconSidebarToggle, "展开侧边栏 (⌘\\)", false) {
						r.mu.Lock()
						r.sidebarCollapsed = false
						r.mu.Unlock()
					}
				}
				ui.Text(c, navCategory(activeNav)).FontSize(12).TextColor(pal.Muted).SingleLine()
				ui.Text(c, "/").FontSize(12).TextColor(pal.SubtleText).SingleLine()
				ui.Text(c, navTitle(activeNav)).FontSize(14).Bold().TextColor(pal.Text).SingleLine()

				safetyTxt := safetyStatusLabel(snap)
				dotColor := pal.Warning
				badgeBg := pal.WarningSoft
				badgeFg := pal.Warning
				if snap.Status.Safety.Gate != nil && snap.Status.Safety.Gate.Mode == "open" {
					dotColor = pal.Success
					badgeBg = pal.SuccessSoft
					badgeFg = pal.Success
				}
				ui.Row(c).Background(badgeBg).Radius(999).Padding(3, 10).Gap(5).AlignItems(ui.Center).Children(func() {
					StatusDot(c, dotColor, 6)
					ui.Text(c, safetyTxt).FontSize(11).Bold().TextColor(badgeFg).SingleLine()
				})

				if snap.Status.EnvironmentUpdateRequired {
					if CompactChipButton(c, pal, "tb_env_update", nil, "组件需更新", false, false) {
						r.SelectNav(NavSettings)
					}
				}
			})

			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				if snap.Status.Workspace != "" {
					ui.MenuButton(c, "项目操作", func(m *ui.Menu) {
						ws := snap.Status.Workspace
						if m.Item("刷新项目文件缓存").Chosen() {
							go r.openOrSaveProject("/api/projects/refresh", ws)
						}
						if m.Item("保存项目状态").Chosen() {
							go r.openOrSaveProject("/api/projects/save", ws)
						}
						m.Separator()
						if m.Item("移除当前项目授权").Danger().Chosen() {
							go r.openOrSaveProject("/api/projects/remove", ws)
						}
					})
				}
				if snap.Status.GuestSystem == "macos" && snap.Status.VMRunning {
					if ui.Button(c, "客机登录画面").Clicked() {
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
					if ui.Button(c, lbl).Clicked() {
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
	var openTerms []core.TerminalSummary
	for _, t := range snap.Terminals {
		if !t.Closed {
			openTerms = append(openTerms, t)
		}
	}

	r.mu.Lock()
	activeID := r.activeTermID
	r.mu.Unlock()
	foundActive := false
	for _, t := range openTerms {
		if t.ID == activeID {
			foundActive = true
			break
		}
	}
	if !foundActive && len(openTerms) > 0 {
		activeID = openTerms[0].ID
		r.mu.Lock()
		r.activeTermID = activeID
		r.mu.Unlock()
	}

	r.workbench.mu.Lock()
	showCLI := r.workbench.showCLISessions
	selAcct := r.workbench.selectedCLIAcct
	r.workbench.mu.Unlock()

	ui.Column(c).Fill().Background(pal.Bg).Gap(0).Children(func() {
		// Terminal tab & action bar
		ui.Row(c).
			FillWidth().
			Height(38).
			Background(pal.SubtleBg).
			BorderWidth(0, 0, 1, 0).
			BorderColor(pal.Border).
			Padding(4, 12).
			Justify(ui.SpaceBetween).
			AlignItems(ui.Center).
			Gap(8).
			Children(func() {
				ui.Row(c).Gap(4).AlignItems(ui.Center).Grow(1).Children(func() {
					for _, t := range openTerms {
						term := t
						lbl := term.Title
						if lbl == "" {
							if term.Kind == "claude" {
								lbl = "Claude"
							} else {
								lbl = "Shell"
							}
						}
						if NativeTabChip(c, pal, "term_"+term.ID, IconTerminal, lbl, term.ID == activeID, func() {
							id := term.ID
							go func() {
								_ = r.Client.DeleteJSON(context.Background(), "/api/terminals/"+id, nil)
								r.Store.TriggerRefresh()
							}()
						}) {
							r.mu.Lock()
							r.activeTermID = term.ID
							r.mu.Unlock()
						}
					}
				})

				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					if len(snap.ClaudePanel.Accounts) > 0 {
						acctLabel := "默认 Claude 账号"
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
					if ui.Button(c, "+ Shell").Disabled(!snap.Status.Ready).Clicked() {
						r.CreateTerminal("shell")
					}
					if ui.PrimaryButton(c, "+ Claude Code").Disabled(!snap.Status.Ready).Clicked() {
						r.CreateTerminal("claude")
					}
					if activeID != "" {
						if ui.Button(c, "传文件").Clicked() {
							go r.uploadFilesToTerminal(activeID)
						}
					}
					cliLbl := "CLI 会话"
					if showCLI {
						cliLbl = "隐藏会话"
					}
					if ui.Button(c, cliLbl).Clicked() {
						r.workbench.mu.Lock()
						r.workbench.showCLISessions = !r.workbench.showCLISessions
						r.workbench.mu.Unlock()
					}
				})
			})

		if !snap.Status.Ready {
			ui.Column(c).FillWidth().Grow(1).Center().Padding(32).Gap(10).Children(func() {
				ui.Icon(c, IconTerminal).FontSize(28).TextColor(pal.Muted)
				ui.Text(c, "环境尚未启动").FontSize(15).Bold().TextColor(pal.Text)
				ui.Text(c, "请先在左侧点击「启动环境」或确认初始化完成。").FontSize(12).TextColor(pal.Muted)
			})
			return
		}

		// Main area: Ghostty Terminal + optional CLI Sessions drawer
		ui.Row(c).FillWidth().Grow(1).Gap(0).Children(func() {
			ui.Column(c).Grow(1).FillHeight().Padding(8, 10).Gap(6).Children(func() {
				if activeID == "" {
					ui.Column(c).Fill().Center().Gap(10).Children(func() {
						ui.Icon(c, IconTerminal).FontSize(28).TextColor(pal.Muted)
						ui.Text(c, "暂无打开的终端").FontSize(15).Bold().TextColor(pal.Text)
						ui.Text(c, "点击上方「+ Shell」或「+ Claude Code」在安全隔离的 Cage 客机中开启终端。").FontSize(12).TextColor(pal.Muted)
					})
				} else {
					sess, err := r.Terminals.GetOrOpen(context.Background(), activeID)
					if err != nil {
						ErrorBanner(c, pal, "终端连接失败："+err.Error())
					} else if sess != nil {
						if sess.CLIStarting() {
							ui.Row(c).Background(pal.AccentSoft).Border(1, pal.Accent).Radius(6).Padding(6, 10).Gap(8).AlignItems(ui.Center).Children(func() {
								ui.Spinner(c)
								ui.Text(c, "Claude CLI 正在启动并载入安全沙盒…").FontSize(12).TextColor(pal.Accent)
							})
						}
						if loginURL := sess.DetectedLoginURL(); loginURL != "" {
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
						if lerr := sess.LastError(); lerr != "" {
							ErrorBanner(c, pal, lerr)
						}
						ui.Box(c).FillWidth().Grow(1).Radius(8).Border(1, pal.Border).Children(func() {
							sess.View(c)
						})
					}
				}
			})

			if showCLI {
				ui.Column(c).Width(280).FillHeight().Background(pal.CardBg).Border(1, pal.Border).Radius(8).Padding(10).Gap(8).Children(func() {
					ui.Text(c, "历史与托管 CLI 会话").FontSize(13).Bold().TextColor(pal.Text)
					ui.Scroll(c).FillWidth().Grow(1).Children(func() {
						ui.Column(c).Gap(6).FillWidth().Children(func() {
							if len(snap.CLISessions) == 0 {
								ui.Text(c, "暂无记录的 CLI 会话").FontSize(12).TextColor(pal.Muted)
							}
							for _, cs := range snap.CLISessions {
								item := cs
								ui.Column(c).Background(pal.ElevatedBg).Border(1, pal.Border).Radius(6).Padding(8).Gap(4).Children(func() {
									title := item.Title
									if title == "" {
										title = filepath.Base(item.Workspace)
									}
									if title == "" {
										title = item.ID
									}
									ui.Text(c, title).FontSize(12).Bold().TextColor(pal.Text)
									if item.AccountLabel != "" {
										ui.Text(c, "账号："+item.AccountLabel).FontSize(11).TextColor(pal.Muted)
									}
									ui.Row(c).Gap(4).Wrap().Children(func() {
										if !item.Archived {
											if ui.Button(c, "在终端打开").Clicked() {
												id := item.ID
												go func() {
													if err := r.Client.PostJSON(context.Background(), "/api/cli/sessions/"+id+"/open", map[string]any{}, nil); err != nil {
														r.PushToast(err.Error())
													}
													r.Store.TriggerRefresh()
												}()
											}
											if ui.Button(c, "归档").Clicked() {
												id := item.ID
												go r.postSimple("/api/cli/sessions/"+id+"/archive", nil)
											}
										} else {
											if ui.Button(c, "恢复").Clicked() {
												id := item.ID
												go r.postSimple("/api/cli/sessions/"+id+"/restore", nil)
											}
											if ui.Button(c, "彻底删除").Clicked() {
												id := item.ID
												go r.postSimple("/api/cli/sessions/"+id+"/purge", nil)
											}
										}
									})
								})
							}
						})
					})
				})
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
