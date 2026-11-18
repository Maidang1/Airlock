package views

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/egoist/mygo/ui"
	"github.com/sylvinus/agent-vm/internal/mygoclient/core"
)

type sidebarSessionItem struct {
	id         string
	termID     string
	title      string
	kind       string
	running    bool
	closed     bool
	isOpenTerm bool
}

func (r *RootView) renderSidebar(c *ui.Context, pal Palette, snap core.Snapshot) {
	r.mu.Lock()
	selectedNav := r.activeNav
	r.mu.Unlock()

	if IsSettingsNav(selectedNav) {
		r.renderSettingsSidebar(c, pal, snap, selectedNav)
		return
	}

	sidebarBg := pal.SidebarBg
	if c.Vibrancy() {
		if pal.Dark {
			sidebarBg = ui.RGBA(32, 32, 35, 0.56)
		} else {
			sidebarBg = ui.RGBA(239, 239, 244, 0.62)
		}
	}

	bar := c.TitleBar()
	leftPad := float32(14)
	if bar.Left > 0 {
		leftPad = bar.Left + 6
	}

	r.mu.Lock()
	activeTermID := r.activeTermID
	expandedCopy := make(map[string]bool, len(r.expandedProjects))
	for k, v := range r.expandedProjects {
		expandedCopy[k] = v
	}
	r.mu.Unlock()

	// Build ordered unique project list.
	var projects []string
	seenProj := map[string]bool{}
	for _, p := range snap.Status.Projects {
		if p != "" && !seenProj[p] {
			seenProj[p] = true
			projects = append(projects, p)
		}
	}
	if snap.Status.Workspace != "" && !seenProj[snap.Status.Workspace] {
		seenProj[snap.Status.Workspace] = true
		projects = append([]string{snap.Status.Workspace}, projects...)
	}

	ui.Column(c).Fill().Background(sidebarBg).BorderWidth(0, 1, 0, 0).BorderColor(pal.Border).Children(func() {
		// 1. Magpie-style macOS titlebar strip (traffic lights + sidebar toggle + brand)
		ui.Row(c).
			FillWidth().
			Height(44).
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
				ui.Icon(c, IconCageBrand).FontSize(14).TextColor(pal.Text)
				ui.Text(c, "Airlock").FontSize(13).Bold().TextColor(pal.Text).SingleLine()
			})

		// 2. Top workspace navigation (Magpie "使用" group)
		ui.Column(c).FillWidth().Padding(2, 8, 6, 8).Gap(2).Children(func() {
			r.sidebarGroupLabel(c, pal, "使用")
			isHomeNav := selectedNav == NavTerminal && activeTermID == ""
			homeNavKey := "__none__"
			if isHomeNav {
				homeNavKey = selectedNav
			}
			r.sidebarNavItem(c, pal, homeNavKey, NavTerminal, IconTerminal, "工作台概览", nil)
			r.sidebarNavItem(c, pal, selectedNav, NavBrowser, IconBrowser, "隔离浏览器", func() {
				if len(snap.BrowserTabs.Tabs) > 0 {
					r.sidebarCountPill(c, pal, fmt.Sprintf("%d", len(snap.BrowserTabs.Tabs)))
				}
			})
		})

		// 3. Projects header with compact add-project icon
		ui.Row(c).FillWidth().Padding(6, 10, 3, 16).Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "项目").FontSize(11).TextColor(pal.SubtleText).SingleLine()
			if IconButton(c, pal, "sb_add_proj", IconFolderPlus, "添加项目目录", false) {
				go r.pickAndOpenProject()
			}
		})

		// 4. Scrollable Project + CLI Session tree (ChatGPT / Codex style)
		ui.Scroll(c).FillWidth().Grow(1).Padding(2, 8, 8, 8).Children(func() {
			ui.Column(c).FillWidth().Gap(10).Children(func() {
				if len(projects) == 0 {
					ui.Column(c).FillWidth().Padding(10, 8).Gap(6).Children(func() {
						ui.Text(c, "尚未授权项目目录").FontSize(12).TextColor(pal.Muted)
						if CompactChipButton(c, pal, "sb_empty_add_proj", IconFolderPlus, "添加项目", false, false) {
							go r.pickAndOpenProject()
						}
					})
					return
				}

				for _, projPath := range projects {
					proj := projPath
					projName := filepath.Base(proj)
					if projName == "" || projName == "." {
						projName = proj
					}
					isProjHome := selectedNav == NavTerminal && activeTermID == "" && snap.Status.Workspace == proj

					// Gather sessions belonging to this project.
					var items []sidebarSessionItem
					openTermIDs := map[string]bool{}
					for _, t := range snap.Terminals {
						wsMatch := t.Workspace == proj || (t.Workspace == "" && proj == snap.Status.Workspace)
						if !wsMatch {
							continue
						}
						lbl := t.Title
						if lbl == "" {
							if t.Kind == "claude" {
								lbl = "Claude 终端"
							} else {
								lbl = "CLI · " + projName
							}
						}
						if t.Closed {
							lbl += "（已结束）"
						}
						openTermIDs[t.ID] = true
						items = append(items, sidebarSessionItem{
							id:         t.ID,
							termID:     t.ID,
							title:      lbl,
							kind:       t.Kind,
							running:    !t.Closed,
							closed:     t.Closed,
							isOpenTerm: true,
						})
					}
					for _, cs := range snap.CLISessions {
						if cs.Archived || cs.Workspace != proj {
							continue
						}
						if cs.TerminalID != "" && openTermIDs[cs.TerminalID] {
							continue
						}
						lbl := cs.Title
						if lbl == "" {
							lbl = cs.ID
						}
						items = append(items, sidebarSessionItem{
							id:         cs.ID,
							termID:     cs.TerminalID,
							title:      lbl,
							kind:       "claude",
							running:    cs.Running,
							isOpenTerm: false,
						})
					}

					ui.Column(c).FillWidth().Gap(2).Children(func() {
						// Project header row
						projRow := ui.Row(c.Key("proj_row_"+proj)).
							FillWidth().
							Height(30).
							Padding(0, 6, 0, 8).
							Radius(8).
							Gap(6).
							AlignItems(ui.Center)
						if isProjHome {
							projRow.Background(pal.ActiveNavBg)
						} else if projRow.Hovered() {
							projRow.Background(pal.HoverBg)
						}

						projRow.Children(func() {
							trigger := ui.ButtonBase(c.Key("proj_btn_"+proj)).
								Grow(1).
								Shrink(1).
								Height(26).
								Gap(7).
								AlignItems(ui.Center).
								Label(projName)
							iconFg := pal.Muted
							txtFg := pal.Text
							if isProjHome {
								iconFg = pal.Accent
							}
							trigger.Children(func() {
								ui.Icon(c, IconFolder).FontSize(13).TextColor(iconFg)
								lbl := ui.Text(c, projName).FontSize(13).TextColor(txtFg).Grow(1).SingleLine().Shrink(1)
								if isProjHome {
									lbl.Bold()
								}
							})
							if trigger.Clicked() {
								r.SelectProjectHome(proj)
							}

							moreBtn := ui.ButtonBase(c.Key("proj_more_" + proj)).
								Size(22, 22).
								Center().
								Radius(5).
								Label("项目操作").
								Tooltip("项目操作")
							if moreBtn.Hovered() {
								moreBtn.Background(pal.HoverBg)
							}
							moreBtn.Children(func() {
								ui.Icon(c, IconMoreDots).FontSize(12).TextColor(pal.Muted)
							})
							moreBtn.Menu(func(m *ui.Menu) {
								if m.Item("打开项目主页").Chosen() {
									r.SelectProjectHome(proj)
								}
								if m.Item("新建 Claude 终端").Disabled(!snap.Status.Ready).Chosen() {
									r.CreateTerminalForWorkspace("claude", proj)
								}
								if m.Item("新建 CLI").Disabled(!snap.Status.Ready).Chosen() {
									r.CreateTerminalForWorkspace("shell", proj)
								}
								m.Separator()
								if m.Item("刷新项目文件缓存").Chosen() {
									go r.openOrSaveProject("/api/projects/refresh", proj)
								}
								if m.Item("保存项目状态").Chosen() {
									go r.openOrSaveProject("/api/projects/save", proj)
								}
								m.Separator()
								if m.Item("移除项目授权").Danger().Chosen() {
									go r.openOrSaveProject("/api/projects/remove", proj)
								}
							})

							newBtn := ui.ButtonBase(c.Key("proj_new_" + proj)).
								Size(22, 22).
								Center().
								Radius(5).
								Disabled(!snap.Status.Ready).
								Label("新建 Claude 终端").
								Tooltip("新建 Claude 终端")
							if newBtn.Hovered() && snap.Status.Ready {
								newBtn.Background(pal.HoverBg)
							}
							newBtn.Children(func() {
								ui.Icon(c, IconNewSession).FontSize(12).TextColor(pal.Muted)
							})
							if newBtn.Clicked() {
								r.CreateTerminalForWorkspace("claude", proj)
							}
						})

						// Sessions list under project (only shown when sessions exist, keeping sidebar clean)
						if len(items) > 0 {
							ui.Column(c).FillWidth().Padding(0, 2, 0, 18).Gap(1).Children(func() {
								expanded := expandedCopy[proj]
								limit := 4
								visible := items
								if !expanded && len(items) > limit {
									visible = items[:limit]
								}
								for _, it := range visible {
								item := it
								isActive := selectedNav == NavTerminal && item.isOpenTerm && activeTermID == item.termID
								sRow := ui.Row(c.Key("sess_row_"+proj+"_"+item.id)).
									FillWidth().
									Height(27).
									Padding(0, 6, 0, 8).
									Radius(6).
									Gap(6).
									AlignItems(ui.Center)
								if isActive {
									sRow.Background(pal.ActiveNavBg)
								} else if sRow.Hovered() {
									sRow.Background(pal.HoverBg)
								}
								sRow.Children(func() {
									sBtn := ui.ButtonBase(c.Key("sess_btn_"+proj+"_"+item.id)).
										Grow(1).
										Shrink(1).
										Height(25).
										AlignItems(ui.Center).
										Label(item.title)
									fg := pal.Muted
									if item.closed {
										fg = pal.SubtleText
									}
									if isActive || sRow.Hovered() {
										fg = pal.Text
									}
									sBtn.Children(func() {
										txt := ui.Text(c, item.title).FontSize(12).TextColor(fg).Grow(1).SingleLine().Shrink(1)
										if isActive {
											txt.Bold()
										}
									})
									if sBtn.Clicked() {
										if item.isOpenTerm {
											r.mu.Lock()
											r.activeTermID = item.termID
											r.activeNav = NavTerminal
											r.mu.Unlock()
											if r.Invalidate != nil {
												r.Invalidate()
											}
										} else {
											id := item.id
											go func() {
												if err := r.Client.PostJSON(context.Background(), "/api/cli/sessions/"+id+"/open", map[string]any{}, nil); err != nil {
													r.PushToast(err.Error())
												}
												r.Store.TriggerRefresh()
											}()
										}
									}

									if item.isOpenTerm && (item.closed || sRow.Hovered() || isActive) {
										closeBtn := ui.ButtonBase(c.Key("sess_close_" + item.termID)).
											Size(16, 16).
											Center().
											Radius(4).
											Label("关闭终端").
											Tooltip("关闭终端")
										if closeBtn.Hovered() {
											closeBtn.Background(pal.HoverBg)
										}
										closeBtn.Children(func() {
											ui.Icon(c, IconClose).FontSize(10).TextColor(pal.Muted)
										})
										if closeBtn.Clicked() {
											r.CloseTerminal(item.termID)
										}
									} else if item.running {
										StatusDot(c, pal.Accent, 6)
									}
								})
							}
							if len(items) > limit {
								moreLbl := "展开显示"
								if expanded {
									moreLbl = "收起"
								}
								expBtn := ui.ButtonBase(c.Key("proj_expand_" + proj)).
									Padding(2, 8).
									Radius(5).
									Label(moreLbl)
								expBtn.Children(func() {
									ui.Text(c, moreLbl).FontSize(11).TextColor(pal.SubtleText).SingleLine()
								})
								if expBtn.Clicked() {
									r.mu.Lock()
									if r.expandedProjects == nil {
										r.expandedProjects = map[string]bool{}
									}
									r.expandedProjects[proj] = !expanded
									r.mu.Unlock()
								}
							}
							})
						}
					})
				}
			})
		})

		// 5. Magpie-style bottom footer: Settings entry + compact gateway status bar
		ui.Column(c).
			FillWidth().
			BorderWidth(1, 0, 0, 0).
			BorderColor(pal.Border).
			Padding(6, 8, 10, 8).
			Gap(4).
			Children(func() {
				if phase, _ := snap.Update["phase"].(string); phase == "available" || phase == "downloading" || phase == "ready" || phase == "failed" {
					msg, _ := snap.Update["message"].(string)
					ui.Column(c).FillWidth().Background(pal.AccentSoft).Border(1, pal.Accent).Radius(8).Padding(7, 9).Gap(4).Children(func() {
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

				// Magpie-style "设置" nav row above the status line
				settingsRow := ui.ButtonBase(c.Key("sb_settings")).
					FillWidth().
					Height(30).
					Padding(0, 10).
					Radius(7).
					Gap(8).
					AlignItems(ui.Center).
					Label("设置")
				if settingsRow.Hovered() {
					settingsRow.Background(pal.HoverBg)
				}
				settingsRow.Children(func() {
					ui.Icon(c, IconSettings).FontSize(14).TextColor(pal.Muted)
					ui.Text(c, "设置").FontSize(13).TextColor(pal.Muted).Grow(1).SingleLine()
					muteLabel := "静音"
					muteIcon := IconVolume
					if snap.ClientPrefs.AudioMuted {
						muteLabel = "已静音"
						muteIcon = IconVolumeMute
					}
					muteBtn := ui.ButtonBase(c.Key("sb_mute")).
						Size(22, 22).
						Center().
						Radius(5).
						Label(muteLabel).
						Tooltip(muteLabel)
					if snap.ClientPrefs.AudioMuted {
						muteBtn.Background(pal.AccentSoft)
					} else if muteBtn.Hovered() {
						muteBtn.Background(pal.HoverBg)
					}
					muteFg := pal.SubtleText
					if snap.ClientPrefs.AudioMuted {
						muteFg = pal.Accent
					}
					muteBtn.Children(func() {
						ui.Icon(c, muteIcon).FontSize(12).TextColor(muteFg)
					})
					if muteBtn.Clicked() {
						r.Store.UpdateClientPreferences(func(p *core.ClientPreferences) {
							p.AudioMuted = !p.AudioMuted
						})
					}
				})
				if settingsRow.Clicked() {
					r.OpenSettings()
				}

				connName := "本机 Airlock"
				if snap.Context.Mode == "remote" {
					if snap.Context.Name != "" {
						connName = snap.Context.Name
					} else {
						connName = "远程主机"
					}
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

				ui.Row(c).FillWidth().Padding(2, 10, 0, 10).Gap(6).AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
					ui.Row(c).Gap(6).AlignItems(ui.Center).Grow(1).Shrink(1).Children(func() {
						StatusDot(c, dotColor, 6)
						ui.Text(c, connName+" · "+statusLabel).FontSize(11).TextColor(pal.Muted).SingleLine().Shrink(1)
					})
					if !snap.Status.VMRunning {
						if CompactChipButton(c, pal, "sb_start_vm", nil, "启动环境", false, snap.Status.Job.Running) {
							go r.postSimple("/api/start", map[string]any{})
						}
					} else {
						if CompactChipButton(c, pal, "sb_stop_vm", nil, "停止", false, snap.Status.Job.Running) {
							go r.postSimple("/api/stop", nil)
						}
					}
				})
			})
	})
}

func (r *RootView) renderSettingsSidebar(c *ui.Context, pal Palette, snap core.Snapshot, selectedNav string) {
	sidebarBg := pal.SidebarBg
	if c.Vibrancy() {
		if pal.Dark {
			sidebarBg = ui.RGBA(32, 32, 35, 0.56)
		} else {
			sidebarBg = ui.RGBA(239, 239, 244, 0.62)
		}
	}

	bar := c.TitleBar()
	leftPad := float32(14)
	if bar.Left > 0 {
		leftPad = bar.Left + 6
	}

	ui.Column(c).Fill().Background(sidebarBg).BorderWidth(0, 1, 0, 0).BorderColor(pal.Border).Children(func() {
		ui.Row(c).
			FillWidth().
			Height(46).
			Padding(0, 10, 0, leftPad).
			Justify(ui.SpaceBetween).
			AlignItems(ui.Center).
			DragWindow().
			Children(func() {
				if CompactChipButton(c, pal, "sb_settings_back", IconArrowLeft, "返回主页", false, false) {
					r.CloseSettings()
				}
			})

		ui.Box(c).FillWidth().Padding(4, 14, 6, 14).Children(func() {
			ui.Text(c, "Airlock 设置").FontSize(13).Bold().TextColor(pal.Text).SingleLine()
		})

		ui.Scroll(c).FillWidth().Grow(1).Padding(2, 8, 8, 8).Children(func() {
			ui.Column(c).FillWidth().Gap(2).Children(func() {
				r.sidebarGroupLabel(c, pal, "网络与环境")
				r.sidebarNavItem(c, pal, selectedNav, NavProxy, IconProxy, "网络代理与节点", func() {
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

				r.sidebarGroupLabel(c, pal, "账号与数据")
				r.sidebarNavItem(c, pal, selectedNav, NavClaudeAccounts, IconAccounts, "Claude 账号调度", nil)
				r.sidebarNavItem(c, pal, selectedNav, NavBrowserMgmt, IconVault, "保险箱与文件管理", func() {
					if len(snap.DesktopFileRequests) > 0 {
						r.sidebarCountPill(c, pal, fmt.Sprintf("%d", len(snap.DesktopFileRequests)))
					}
				})

				r.sidebarGroupLabel(c, pal, "系统与诊断")
				r.sidebarNavItem(c, pal, selectedNav, NavSettings, IconSettings, "通用、资源与诊断", nil)
			})
		})

		ui.Row(c).
			FillWidth().
			BorderWidth(1, 0, 0, 0).
			BorderColor(pal.Border).
			Padding(10, 14).
			Justify(ui.SpaceBetween).
			AlignItems(ui.Center).
			Children(func() {
				ver := snap.Build.Version
				if ver == "" {
					ver = "Airlock"
				} else {
					ver = "Airlock " + ver
				}
				ui.Text(c, ver).FontSize(11).TextColor(pal.Muted).SingleLine()
				ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
					StatusDot(c, pal.Success, 6)
					ui.Text(c, "服务正常").FontSize(11).TextColor(pal.Success).SingleLine()
				})
			})
	})
}

func (r *RootView) sidebarGroupLabel(c *ui.Context, pal Palette, title string) {
	ui.Box(c).FillWidth().Padding(10, 10, 4, 10).Children(func() {
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
		Title:     "选择要授权给 Airlock 的项目目录",
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
