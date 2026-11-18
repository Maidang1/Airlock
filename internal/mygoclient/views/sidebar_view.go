package views

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/egoist/mygo/ui"
	"github.com/sylvinus/agent-vm/internal/mygoclient/core"
)

func (r *RootView) renderSidebar(c *ui.Context, pal Palette, snap core.Snapshot) {
	sidebarBg := pal.SidebarBg
	if c.Vibrancy() {
		if pal.Dark {
			sidebarBg = ui.RGBA(35, 35, 38, 0.48)
		} else {
			sidebarBg = ui.RGBA(235, 235, 240, 0.52)
		}
	}

	bar := c.TitleBar()
	leftPad := float32(14)
	if bar.Left > 0 {
		leftPad = bar.Left + 6
	}

	ui.Column(c).Fill().Background(sidebarBg).BorderWidth(0, 1, 0, 0).BorderColor(pal.Border).Children(func() {
		// 1. Integrated macOS titlebar strip (traffic lights + sidebar toggle + brand)
		ui.Row(c).
			FillWidth().
			Height(48).
			Padding(0, 10, 0, leftPad).
			Gap(6).
			AlignItems(ui.Center).
			DragWindow().
			Children(func() {
				if IconButton(c, pal, "sb_collapse", IconSidebarToggle, "收起侧边栏 (⌘\\)", false) {
					r.mu.Lock()
					r.sidebarCollapsed = true
					r.mu.Unlock()
				}
				ui.Icon(c, IconCageBrand).FontSize(15).TextColor(pal.Accent)
				ui.Text(c, "Cage").FontSize(13).Bold().TextColor(pal.Text).SingleLine()
			})

		// 2. Compact active project switcher (macOS source list capsule)
		ws := snap.Status.Workspace
		wsName := "未选择项目"
		if ws != "" {
			wsName = filepath.Base(ws)
		}
		ui.Box(c).FillWidth().Padding(2, 10, 8, 10).Children(func() {
			ui.Row(c).
				FillWidth().
				Height(32).
				Background(pal.ElevatedBg).
				Border(1, pal.Border).
				Radius(8).
				Padding(0, 6, 0, 8).
				Gap(6).
				AlignItems(ui.Center).
				Children(func() {
					projBtn := ui.ButtonBase(c.Key("sb_proj_label")).
						Grow(1).
						Height(26).
						Padding(0, 4).
						Radius(5).
						Gap(6).
						AlignItems(ui.Center).
						Label(wsName)
					if projBtn.Hovered() {
						projBtn.Background(pal.HoverBg)
					}
					projBtn.Children(func() {
						ui.Icon(c, IconFolder).FontSize(13).TextColor(pal.Accent)
						ui.Text(c, wsName).FontSize(12).Bold().TextColor(pal.Text).Grow(1).SingleLine()
					})
					if projBtn.Clicked() {
						go r.pickAndOpenProject()
					}
					if len(snap.Status.Projects) > 0 {
						ui.MenuButton(c, "切换", func(m *ui.Menu) {
							for _, p := range snap.Status.Projects {
								proj := p
								if m.Item(filepath.Base(proj)).Checked(proj == ws).Chosen() {
									go r.activateProject(proj, false)
								}
							}
						})
					}
					if IconButton(c, pal, "sb_add_proj", IconPlus, "添加项目目录", false) {
						go r.pickAndOpenProject()
					}
				})
		})

		// 3. Scrollable Magpie-style source list navigation
		r.mu.Lock()
		selectedNav := r.activeNav
		r.mu.Unlock()

		activeTerms := 0
		for _, t := range snap.Terminals {
			if !t.Closed {
				activeTerms++
			}
		}

		ui.Scroll(c).FillWidth().Grow(1).Padding(2, 8, 8, 8).Children(func() {
			ui.Column(c).FillWidth().Gap(2).Children(func() {
				r.sidebarGroupLabel(c, pal, "工作区")
				r.sidebarNavItem(c, pal, selectedNav, NavTerminal, IconTerminal, "终端与 Claude", func() {
					if activeTerms > 0 {
						r.sidebarCountPill(c, pal, fmt.Sprintf("%d", activeTerms))
					}
				})
				r.sidebarNavItem(c, pal, selectedNav, NavBrowser, IconBrowser, "隔离浏览器", func() {
					if len(snap.BrowserTabs.Tabs) > 0 {
						r.sidebarCountPill(c, pal, fmt.Sprintf("%d", len(snap.BrowserTabs.Tabs)))
					}
				})
				r.sidebarNavItem(c, pal, selectedNav, NavClaudeDesktop, IconClaudeDesktop, "Claude Desktop", func() {
					if snap.ClaudeDesktop.Running {
						StatusDot(c, pal.Success, 6)
					}
				})

				r.sidebarGroupLabel(c, pal, "网络与环境")
				r.sidebarNavItem(c, pal, selectedNav, NavProxy, IconProxy, "网络代理", func() {
					if snap.Proxy.NetworkOpen {
						StatusDot(c, pal.Success, 6)
					} else if snap.Proxy.StateLabel != "" {
						StatusDot(c, pal.Warning, 6)
					}
				})
				r.sidebarNavItem(c, pal, selectedNav, NavSafety, IconShield, "安全检查与出口", nil)
				r.sidebarNavItem(c, pal, selectedNav, NavTailscale, IconTailscale, "Tailscale 私网", func() {
					if snap.Tailscale.Connected {
						StatusDot(c, pal.Success, 6)
					}
				})
				r.sidebarNavItem(c, pal, selectedNav, NavRemoteHosts, IconCloudSSH, "远程 Cloud SSH", func() {
					if len(snap.Hosts) > 0 {
						r.sidebarCountPill(c, pal, fmt.Sprintf("%d", len(snap.Hosts)))
					}
				})

				r.sidebarGroupLabel(c, pal, "管理与设置")
				r.sidebarNavItem(c, pal, selectedNav, NavClaudeAccounts, IconAccounts, "Claude 账号调度", nil)
				r.sidebarNavItem(c, pal, selectedNav, NavBrowserMgmt, IconVault, "保险箱与文件管理", func() {
					if len(snap.DesktopFileRequests) > 0 {
						r.sidebarCountPill(c, pal, fmt.Sprintf("%d", len(snap.DesktopFileRequests)))
					}
				})
				r.sidebarNavItem(c, pal, selectedNav, NavSettings, IconSettings, "设置与诊断", nil)
			})
		})

		// 4. Bottom-pinned environment status control pod
		ui.Column(c).
			FillWidth().
			BorderWidth(1, 0, 0, 0).
			BorderColor(pal.Border).
			Padding(10, 10).
			Gap(8).
			Children(func() {
				if phase, _ := snap.Update["phase"].(string); phase == "available" || phase == "downloading" || phase == "ready" || phase == "failed" {
					msg, _ := snap.Update["message"].(string)
					ui.Column(c).FillWidth().Background(pal.AccentSoft).Border(1, pal.Accent).Radius(8).Padding(8, 10).Gap(4).Children(func() {
						ui.Text(c, "发现应用更新："+phase).FontSize(11).Bold().TextColor(pal.Accent)
						if msg != "" {
							ui.Text(c, msg).FontSize(11).TextColor(pal.Text)
						}
						if phase == "available" || phase == "ready" || phase == "failed" {
							if CompactChipButton(c, pal, "sb_update_view", nil, "查看并安装", true, false) {
								r.SelectNav(NavSettings)
							}
						}
					})
				}

				connName := "本机 Cage"
				if snap.Context.Mode == "remote" {
					if snap.Context.Name != "" {
						connName = snap.Context.Name
					} else {
						connName = "远程主机"
					}
				}
				guestLabel := "Linux"
				if snap.Status.GuestSystem != "" {
					guestLabel = snap.Status.GuestSystem
				}
				dotColor := pal.SubtleText
				statusLabel := "已停止"
				if snap.Status.Ready && snap.Status.VMRunning {
					dotColor = pal.Success
					statusLabel = "运行中"
				} else if snap.Status.Job.Running {
					dotColor = pal.Warning
					statusLabel = "处理中"
				}

				ui.Column(c).
					FillWidth().
					Background(pal.CardBg).
					Border(1, pal.Border).
					Radius(9).
					Padding(10).
					Gap(8).
					Children(func() {
						ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).Children(func() {
							StatusDot(c, dotColor, 7)
							ui.Text(c, connName).FontSize(12).Bold().TextColor(pal.Text).SingleLine()
							ui.Text(c, guestLabel).FontSize(11).TextColor(pal.Muted).Grow(1).SingleLine()
							ui.Text(c, statusLabel).FontSize(11).TextColor(pal.Muted).SingleLine()
						})

						ui.Row(c).FillWidth().Gap(6).Wrap().AlignItems(ui.Center).Children(func() {
							if !snap.Status.VMRunning {
								if ui.PrimaryButton(c, "启动环境").Disabled(snap.Status.Job.Running).Clicked() {
									go r.postSimple("/api/start", map[string]any{})
								}
							} else {
								if ui.Button(c, "停止").Disabled(snap.Status.Job.Running).Clicked() {
									go r.postSimple("/api/stop", nil)
								}
							}
							if snap.Status.CanRestore {
								if ui.Button(c, "恢复备份").Disabled(snap.Status.Job.Running).Clicked() {
									go r.postSimple("/api/restore", nil)
								}
							}
							muteLabel := "静音"
							if snap.ClientPrefs.AudioMuted {
								muteLabel = "已静音"
							}
							if ui.Button(c, muteLabel).Clicked() {
								r.Store.UpdateClientPreferences(func(p *core.ClientPreferences) {
									p.AudioMuted = !p.AudioMuted
								})
							}
						})
					})
			})
	})
}

func (r *RootView) sidebarGroupLabel(c *ui.Context, pal Palette, title string) {
	ui.Box(c).FillWidth().Padding(12, 10, 4, 10).Children(func() {
		ui.Text(c, title).FontSize(11).Bold().TextColor(pal.SubtleText).SingleLine()
	})
}

func (r *RootView) sidebarCountPill(c *ui.Context, pal Palette, text string) {
	ui.Box(c).Background(pal.ElevatedBg).Radius(999).Padding(1, 7).Children(func() {
		ui.Text(c, text).FontSize(10).Bold().TextColor(pal.Muted).SingleLine()
	})
}

func (r *RootView) sidebarNavItem(c *ui.Context, pal Palette, selectedNav, id string, icon *ui.SVG, label string, trailing func()) {
	selected := selectedNav == id
	btn := ui.ButtonBase(c.Key("nav_" + id)).
		FillWidth().
		Height(31).
		Padding(0, 10).
		Radius(7).
		Gap(8).
		AlignItems(ui.Center).
		Label(label)

	fg := pal.Muted
	iconFg := pal.Muted
	if selected {
		btn.Background(pal.ActiveNavBg)
		fg = pal.Text
		iconFg = pal.Accent
	} else if btn.Hovered() {
		btn.Background(pal.HoverBg)
		fg = pal.Text
		iconFg = pal.Text
	}

	btn.Children(func() {
		ui.Icon(c, icon).FontSize(14).TextColor(iconFg)
		txt := ui.Text(c, label).FontSize(13).TextColor(fg).Grow(1).SingleLine()
		if selected {
			txt.Bold()
		}
		if trailing != nil {
			trailing()
		}
	})
	if btn.Clicked() {
		r.SelectNav(id)
	}
}

func (r *RootView) pickAndOpenProject() {
	_, _ = r.pickAndActivateProject()
}

func (r *RootView) pickAndActivateProject() (string, bool) {
	if r.Bridge == nil {
		return "", false
	}
	paths, err := r.Bridge.OpenDialog(core.NativeDialogOptions{
		Title:     "选择要授权给 Cage 的项目目录",
		Directory: true,
	})
	if err != nil || len(paths) == 0 {
		return "", false
	}
	chosen := paths[0]
	if r.Store.Snapshot().Context.Mode == "remote" {
		if _, err := r.Client.RunFileCommand(context.Background(), "share-project", "write", chosen); err != nil {
			r.PushToast("共享项目到远程环境失败：" + err.Error())
			return "", false
		}
		r.PushToast("已将本机项目挂载到远程环境")
		r.Store.TriggerRefresh()
		return chosen, true
	}
	if err := r.Client.PostJSON(context.Background(), "/api/projects/open", map[string]any{
		"workspace": chosen,
		"add":       true,
		"activate":  true,
	}, nil); err != nil {
		r.PushToast(err.Error())
		return "", false
	}
	r.Store.TriggerRefresh()
	return chosen, true
}

func (r *RootView) activateProject(workspace string, add bool) {
	if err := r.Client.PostJSON(context.Background(), "/api/projects/open", map[string]any{
		"workspace": workspace,
		"add":       add,
		"activate":  true,
	}, nil); err != nil {
		r.PushToast(err.Error())
	} else {
		r.Store.TriggerRefresh()
	}
}

func (r *RootView) openOrSaveProject(path, workspace string) {
	if err := r.Client.PostJSON(context.Background(), path, map[string]string{"workspace": workspace}, nil); err != nil {
		r.PushToast(err.Error())
	} else {
		r.Store.TriggerRefresh()
	}
}
