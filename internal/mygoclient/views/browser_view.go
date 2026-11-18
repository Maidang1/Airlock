package views

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/sylvinus/agent-vm/internal/mygoclient/core"
)

type browserViewState struct {
	mu           sync.Mutex
	urlInput     string
	lastActiveID string
	busy         bool
}

func (r *RootView) renderBrowserView(c *ui.Context, pal Palette, snap core.Snapshot) {
	st := &r.browser
	tabs := snap.BrowserTabs

	// Keep address bar synced when active tab changes outside manual typing.
	activeTabID := tabs.ActiveID
	var activeURL string
	for _, tb := range tabs.Tabs {
		if tb.Active {
			activeTabID = tb.ID
			activeURL = tb.URL
			break
		}
	}
	if activeTabID == "" && len(tabs.Tabs) > 0 {
		activeTabID = tabs.Tabs[0].ID
		activeURL = tabs.Tabs[0].URL
	}
	st.mu.Lock()
	if activeTabID != st.lastActiveID {
		st.lastActiveID = activeTabID
		if activeURL != "" {
			st.urlInput = activeURL
		}
	}
	st.mu.Unlock()

	ui.Column(c).Fill().Background(pal.Bg).Gap(0).Children(func() {
		if !snap.Status.Ready || !snap.Status.VMRunning {
			ui.Column(c).Fill().Center().Padding(32).Gap(10).Children(func() {
				ui.Icon(c, IconBrowser).FontSize(28).TextColor(pal.Muted)
				ui.Text(c, "隔离浏览器尚未启动").FontSize(15).Bold().TextColor(pal.Text)
				ui.Text(c, "请先在左侧边栏点击「启动环境」并等待就绪。").FontSize(12).TextColor(pal.Muted)
				if ui.PrimaryButton(c, "启动环境").Disabled(snap.Status.Job.Running).Clicked() {
					go r.postSimple("/api/start", map[string]any{})
				}
			})
			return
		}

		// 1. Native browser tab strip + inline VNC connection status
		ui.Row(c).
			FillWidth().
			Height(36).
			Background(pal.SubtleBg).
			BorderWidth(0, 0, 1, 0).
			BorderColor(pal.Border).
			Padding(4, 10).
			Gap(4).
			AlignItems(ui.Center).
			Children(func() {
				for _, tb := range tabs.Tabs {
					tab := tb
					lbl := tab.Title
					if lbl == "" {
						lbl = tab.URL
					}
					if lbl == "" {
						lbl = "新标签页"
					}
					if len([]rune(lbl)) > 24 {
						lbl = string([]rune(lbl)[:23]) + "…"
					}
					tabIcon := IconBrowser
					if tab.Incognito {
						tabIcon = IconIncognito
					}
					var onClose func()
					if len(tabs.Tabs) > 1 {
						id := tab.ID
						onClose = func() {
							go r.browserPost("/api/browser/close", map[string]string{"id": id})
						}
					}
					if NativeTabChip(c, pal, "btab_"+tab.ID, tabIcon, lbl, tab.ID == activeTabID, onClose) {
						go r.browserPost("/api/browser/activate", map[string]string{"id": tab.ID})
					}
				}

				if CompactChipButton(c, pal, "br_new_tab", IconPlus, "标签", false, false) {
					go r.browserPost("/api/browser/new", map[string]any{"url": snap.Settings.BrowserHomeURL})
				}
				if CompactChipButton(c, pal, "br_new_incog", IconIncognito, "无痕", false, false) {
					go r.browserPost("/api/browser/new", map[string]any{"url": snap.Settings.BrowserHomeURL, "incognito": true})
				}

				ui.Spacer(c)

				if snap.Status.GuestSystem != "macos" {
					stVNC := r.VNCBridge.SnapshotState("browser")
					if stVNC.Connection == "connected" && !stVNC.SizeError {
						ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
							StatusDot(c, pal.Success, 6)
							ui.Text(c, "已连接").FontSize(11).TextColor(pal.Muted).SingleLine()
							if IconButton(c, pal, "br_vnc_reconn_ok", IconRefresh, "重连画布", false) {
								r.VNC.Reconnect("browser")
							}
						})
					} else {
						dot := pal.Warning
						msg := "正在连接画面…"
						if stVNC.Connection != "connecting" {
							dot = pal.Danger
							msg = "画面未连接"
							if stVNC.LastError != "" {
								errTxt := stVNC.LastError
								if len([]rune(errTxt)) > 26 {
									errTxt = string([]rune(errTxt)[:25]) + "…"
								}
								msg = errTxt
							} else if stVNC.Status != "" {
								msg = stVNC.Status
							}
						}
						ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
							StatusDot(c, dot, 6)
							ui.Text(c, msg).FontSize(11).TextColor(pal.Muted).SingleLine()
							if CompactChipButton(c, pal, "br_vnc_reconn", IconRefresh, "重连画布", false, false) {
								r.VNC.Reconnect("browser")
							}
						})
					}
				}
			})

		// 2. Unified Safari/Arc-style navigation & omnibox toolbar
		ui.Row(c).
			FillWidth().
			Height(42).
			Background(pal.CardBg).
			BorderWidth(0, 0, 1, 0).
			BorderColor(pal.Border).
			Padding(5, 10).
			Gap(6).
			AlignItems(ui.Center).
			Children(func() {
				if IconButton(c, pal, "br_back", IconArrowLeft, "后退", activeTabID == "") {
					go r.browserPost("/api/browser/back", map[string]string{"id": activeTabID})
				}
				if IconButton(c, pal, "br_fwd", IconArrowRight, "前进", activeTabID == "") {
					go r.browserPost("/api/browser/forward", map[string]string{"id": activeTabID})
				}
				if IconButton(c, pal, "br_reload", IconRefresh, "刷新页面", activeTabID == "") {
					go r.browserPost("/api/browser/reload", map[string]string{"id": activeTabID})
				}

				// Omnibox pill
				ui.Row(c).
					Grow(1).
					Height(30).
					Background(pal.ElevatedBg).
					Border(1, pal.Border).
					Radius(8).
					Padding(0, 4, 0, 10).
					Gap(6).
					AlignItems(ui.Center).
					Children(func() {
						leadIcon := IconBrowser
						if strings.HasPrefix(strings.TrimSpace(st.urlInput), "https://") {
							leadIcon = IconLock
						}
						ui.Icon(c, leadIcon).FontSize(13).TextColor(pal.Muted)
						urlField := ui.TextInputBase(c, &st.urlInput).
							Placeholder("输入网址或搜索词，按回车访问…").
							Grow(1)
						navigate := func() {
							raw := strings.TrimSpace(st.urlInput)
							if raw == "" {
								return
							}
							target := normalizeBrowserURL(raw)
							if activeTabID != "" {
								go r.browserPost("/api/browser/navigate", map[string]string{"id": activeTabID, "url": target})
							} else {
								go r.browserPost("/api/browser/new", map[string]any{"url": target})
							}
						}
						if urlField.Submitted() {
							navigate()
						}
						if CompactChipButton(c, pal, "br_go", nil, "访问", true, strings.TrimSpace(st.urlInput) == "") {
							navigate()
						}
					})

				if CompactChipButton(c, pal, "br_upload", IconUpload, "传文件", false, false) {
					go r.uploadFilesToBrowser(snap)
				}
				if CompactChipButton(c, pal, "br_vault", IconVault, "密码填充", false, activeTabID == "") {
					r.SelectNav(NavBrowserMgmt)
				}
			})

		// 3. Slim bookmarks strip (only when favorites exist)
		if len(snap.Favorites.Items) > 0 {
			ui.Row(c).
				FillWidth().
				Height(28).
				Background(pal.CardBg).
				BorderWidth(0, 0, 1, 0).
				BorderColor(pal.Border).
				Padding(0, 10).
				Gap(4).
				AlignItems(ui.Center).
				Children(func() {
					ui.Text(c, "收藏").FontSize(11).TextColor(pal.SubtleText).SingleLine()
					for _, fav := range snap.Favorites.Items {
						f := fav
						btn := ui.ButtonBase(c.Key("fav_" + f.ID)).
							Height(20).
							Padding(0, 7).
							Radius(5).
							AlignItems(ui.Center).
							Label(f.Title)
						if btn.Hovered() {
							btn.Background(pal.HoverBg)
						}
						btn.Children(func() {
							ui.Text(c, f.Title).FontSize(11).TextColor(pal.Muted).SingleLine()
						})
						if btn.Clicked() {
							if activeTabID != "" {
								go r.browserPost("/api/browser/navigate", map[string]string{"id": activeTabID, "url": f.URL})
							} else {
								go r.browserPost("/api/browser/new", map[string]any{"url": f.URL})
							}
						}
					}
				})
		}

		// 4. Inline upload/download notification strips
		if len(tabs.Uploads) > 0 {
			u := tabs.Uploads[0]
			ui.Row(c).
				FillWidth().
				Background(pal.WarningSoft).
				BorderWidth(0, 0, 1, 0).
				BorderColor(pal.Warning).
				Padding(6, 12).
				Gap(8).
				AlignItems(ui.Center).
				Children(func() {
					ui.Text(c, "网页请求选择上传本机文件").FontSize(12).Bold().TextColor(pal.Text).Grow(1)
					if ui.PrimaryButton(c, "选择文件并上传").Clicked() {
						go r.fulfillBrowserUpload(u)
					}
					if ui.Button(c, "取消").Clicked() {
						go r.browserPost("/api/browser/upload-cancel", map[string]string{"id": u.ID})
					}
				})
		}
		if len(tabs.Downloads) > 0 {
			dl := tabs.Downloads[len(tabs.Downloads)-1]
			ui.Row(c).
				FillWidth().
				Background(pal.SubtleBg).
				BorderWidth(0, 0, 1, 0).
				BorderColor(pal.Border).
				Padding(5, 12).
				Gap(8).
				AlignItems(ui.Center).
				Children(func() {
					ui.Icon(c, IconDownload).FontSize(12).TextColor(pal.Muted)
					ui.Text(c, fmt.Sprintf("最近下载：%s (%s)", dl.Name, dl.State)).FontSize(12).TextColor(pal.Text).Grow(1).SingleLine()
					if dl.State == "saved" || dl.State == "ready" {
						if ui.Button(c, "保存/打开").Clicked() {
							go func() {
								if _, err := r.Client.RunFileCommand(context.Background(), "browser-file", "open", dl.ID); err != nil {
									r.PushToast(err.Error())
								}
							}()
						}
					}
					if ui.Button(c, "全部管理").Clicked() {
						r.SelectNav(NavBrowserMgmt)
					}
				})
		}

		// 5. Full-bleed browser viewport
		if snap.Status.GuestSystem == "macos" {
			ui.Box(c).FillWidth().Grow(1).Background(ui.Hex("#ffffff")).Children(func() {
				r.MacBrowser.View(c)
			})
		} else {
			wv, createErr := r.VNC.SyncSurface(
				"browser",
				activeTabID,
				snap.Context.ConnectionID,
				snap.Settings.Keyboard,
				snap.Status.GuestSystem,
				snap.Settings.ProjectFileCache,
			)
			if createErr != "" {
				ui.Column(c).FillWidth().Grow(1).Center().Padding(24).Gap(10).Children(func() {
					ErrorBanner(c, pal, "无法初始化浏览器画布："+createErr)
					if ui.PrimaryButton(c, "重连画布").Clicked() {
						r.VNC.Reconnect("browser")
					}
				})
				return
			}
			ui.Box(c).FillWidth().Grow(1).Background(ui.Hex("#ffffff")).Children(func() {
				r.VNC.View(c, wv)
			})
		}
	})
}

func (r *RootView) renderClaudeDesktopView(c *ui.Context, pal Palette, snap core.Snapshot) {
	cd := snap.ClaudeDesktop
	ui.Column(c).Fill().Background(pal.Bg).Gap(0).Children(func() {
		dot := pal.SubtleText
		stateText := "未运行"
		if cd.Running {
			dot = pal.Success
			stateText = "运行中"
		} else if cd.Failed {
			dot = pal.Danger
			stateText = "启动异常 " + cd.FailureReason
		} else if !cd.Installed && snap.Status.GuestSystem != "macos" {
			stateText = "未安装"
		}

		ui.Row(c).
			FillWidth().
			Height(42).
			Background(pal.SubtleBg).
			BorderWidth(0, 0, 1, 0).
			BorderColor(pal.Border).
			Padding(0, 14).
			Justify(ui.SpaceBetween).
			AlignItems(ui.Center).
			Children(func() {
				ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
					StatusDot(c, dot, 7)
					ui.Text(c, "Claude Desktop · "+stateText).FontSize(13).Bold().TextColor(pal.Text)
					if cd.Version != "" {
						ui.Text(c, "v"+cd.Version).FontSize(11).TextColor(pal.Muted)
					}
				})

				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					if snap.Status.GuestSystem == "macos" {
						if ui.PrimaryButton(c, "启动 / 唤醒 Claude").Disabled(!snap.Status.Ready).Clicked() {
							go func() {
								if _, err := r.MacClaude.SendWindowAction(context.Background(), "launch"); err != nil {
									r.PushToast("启动 Claude 失败：" + err.Error())
								}
								r.Store.TriggerRefresh()
							}()
						}
						if ui.Button(c, "关闭窗口").Disabled(!cd.Running).Clicked() {
							go func() {
								_, _ = r.MacClaude.SendWindowAction(context.Background(), "stop")
								r.Store.TriggerRefresh()
							}()
						}
					} else {
						if !cd.Installed {
							if ui.PrimaryButton(c, "安装 Claude Desktop").Disabled(snap.Status.Job.Running || !snap.Status.Ready).Clicked() {
								go r.postSimple("/api/claude-desktop/install", map[string]any{})
							}
						} else if !cd.Running {
							if ui.PrimaryButton(c, "启动 Claude Desktop").Disabled(snap.Status.Job.Running || !snap.Status.Ready).Clicked() {
								go r.postSimple("/api/claude-desktop/start", map[string]any{})
							}
							if ui.Button(c, "检查更新").Disabled(snap.Status.Job.Running).Clicked() {
								go r.postSimple("/api/claude-desktop/update", map[string]any{})
							}
						} else {
							if ui.Button(c, "停止 Claude Desktop").Disabled(snap.Status.Job.Running).Clicked() {
								go r.postSimple("/api/claude-desktop/stop", map[string]any{})
							}
							if ui.Button(c, "重连画布").Clicked() {
								r.VNC.Reconnect("desktop")
							}
						}
					}
				})
			})

		if !snap.Status.Ready || !snap.Status.VMRunning {
			ui.Column(c).FillWidth().Grow(1).Center().Padding(32).Gap(10).Children(func() {
				ui.Icon(c, IconClaudeDesktop).FontSize(28).TextColor(pal.Muted)
				ui.Text(c, "环境尚未启动").FontSize(15).Bold().TextColor(pal.Text)
				ui.Text(c, "请先在左侧点击「启动环境」。").FontSize(12).TextColor(pal.Muted)
			})
			return
		}

		if snap.Status.GuestSystem == "macos" {
			if !cd.Running {
				ui.Column(c).FillWidth().Grow(1).Center().Padding(32).Gap(10).Children(func() {
					ui.Icon(c, IconClaudeDesktop).FontSize(28).TextColor(pal.Muted)
					ui.Text(c, "Claude Desktop 未唤醒").FontSize(15).Bold().TextColor(pal.Text)
					ui.Text(c, "点击右上角「启动 / 唤醒 Claude」在 macOS 客机中打开 Claude Desktop。").FontSize(12).TextColor(pal.Muted)
				})
				return
			}
			ui.Box(c).FillWidth().Grow(1).Background(ui.Hex("#ffffff")).Children(func() {
				r.MacClaude.View(c)
			})
			return
		}

		if !cd.Running {
			ui.Column(c).FillWidth().Grow(1).Center().Padding(32).Gap(10).Children(func() {
				ui.Icon(c, IconClaudeDesktop).FontSize(28).TextColor(pal.Muted)
				ui.Text(c, "Claude Desktop 当前未在运行").FontSize(15).Bold().TextColor(pal.Text)
				ui.Text(c, "请点击右上角「启动 Claude Desktop」或「安装 Claude Desktop」。").FontSize(12).TextColor(pal.Muted)
			})
			return
		}

		wv, createErr := r.VNC.SyncSurface(
			"desktop",
			"desktop",
			snap.Context.ConnectionID,
			snap.Settings.Keyboard,
			snap.Status.GuestSystem,
			true,
		)
		if createErr != "" {
			ui.Column(c).FillWidth().Grow(1).Center().Padding(24).Gap(10).Children(func() {
				ErrorBanner(c, pal, "无法初始化 Claude Desktop 画布："+createErr)
				if ui.PrimaryButton(c, "重连画布").Clicked() {
					r.VNC.Reconnect("desktop")
				}
			})
			return
		}
		ui.Box(c).FillWidth().Grow(1).Background(ui.Hex("#ffffff")).Children(func() {
			r.VNC.View(c, wv)
		})
	})
}

func normalizeBrowserURL(raw string) string {
	s := strings.TrimSpace(raw)
	if !strings.Contains(s, "://") {
		if strings.Contains(s, ".") && !strings.Contains(s, " ") {
			return "https://" + s
		}
		return "https://www.bing.com/search?q=" + strings.ReplaceAll(s, " ", "+")
	}
	return s
}

func (r *RootView) browserPost(path string, body any) {
	if err := r.Client.PostJSON(context.Background(), path, body, nil); err != nil {
		r.PushToast(err.Error())
	}
	r.Store.TriggerRefresh()
}

func (r *RootView) uploadFilesToBrowser(snap core.Snapshot) {
	if r.Bridge == nil {
		return
	}
	if len(snap.BrowserTabs.Uploads) > 0 {
		r.fulfillBrowserUpload(snap.BrowserTabs.Uploads[0])
		return
	}
	r.PushToast("请先在网页中点击文件上传控件（如 <input type=file>），再在此上传本机文件。")
}

func (r *RootView) fulfillBrowserUpload(u core.BrowserUpload) {
	if r.Bridge == nil {
		return
	}
	paths, err := r.Bridge.OpenDialog(core.NativeDialogOptions{
		Title:    "选择上传到隔离浏览器的文件",
		Multiple: u.Multiple,
	})
	if err != nil || len(paths) == 0 {
		return
	}
	args := append([]string{"browser", u.ID}, paths...)
	if _, err := r.Client.RunFileCommand(context.Background(), "upload-files", args...); err != nil {
		r.PushToast("文件上传失败：" + err.Error())
	} else {
		r.PushToast("已上传文件至浏览器")
	}
	r.Store.TriggerRefresh()
}

// unused json safeguard keep compiler happy if trimmed
var _ = json.Valid
