package views

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/sylvinus/agent-vm/internal/macnative"
	"github.com/sylvinus/agent-vm/internal/mygoclient/core"
)

type browserExtensionItem struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Enabled bool   `json:"enabled"`
}

type browserMgmtState struct {
	mu           sync.Mutex
	activeTab    int // 0=Vault, 1=Favorites, 2=Extensions, 3=Downloads, 4=Desktop File Requests
	vaultLoaded  bool
	vaultOrigin  string
	vaultUser    string
	vaultPass    string
	revealAcctPW string
	revealedID   string
	revealedPW   string

	favTitle string
	favURL   string

	extensions []browserExtensionItem
	homeURL    string
	loadedHome bool

	busy   bool
	errMsg string
}

func (r *RootView) ensureBrowserMgmtData(snap core.Snapshot) {
	st := &r.browserMgmt
	st.mu.Lock()
	needVault := !st.vaultLoaded
	if needVault {
		st.vaultLoaded = true
	}
	if !st.loadedHome {
		st.homeURL = snap.Settings.BrowserHomeURL
		st.loadedHome = true
	}
	st.mu.Unlock()
	if needVault {
		go func() {
			_ = r.Store.RefreshVault(context.Background())
			_ = r.Store.RefreshSavedDownloads(context.Background())
			r.refreshExtensions()
		}()
	}
}

func (r *RootView) refreshExtensions() {
	var resp struct {
		Extensions []browserExtensionItem `json:"extensions"`
	}
	if err := r.Client.PostJSON(context.Background(), "/api/browser/extensions", map[string]string{"action": "list"}, &resp); err == nil {
		r.browserMgmt.mu.Lock()
		r.browserMgmt.extensions = resp.Extensions
		r.browserMgmt.mu.Unlock()
		r.Invalidate()
	}
}

func (r *RootView) renderBrowserMgmtView(c *ui.Context, pal Palette, snap core.Snapshot) {
	r.ensureBrowserMgmtData(snap)
	st := &r.browserMgmt
	st.mu.Lock()
	tab := st.activeTab
	busy := st.busy
	errMsg := st.errMsg
	revealedID := st.revealedID
	revealedPW := st.revealedPW
	exts := append([]browserExtensionItem(nil), st.extensions...)
	st.mu.Unlock()

	PageScroll(c, pal, 980, 16, func() {
		ui.Column(c).Gap(14).FillWidth().Children(func() {
				Card(c, pal, func() {
					ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
						SectionHeading(c, pal, "浏览器保险箱、扩展、收藏夹与文件传输", "集中管理加密凭据、书签、浏览器扩展包与文件下载打开审批")
						if ui.Button(c, "刷新全部").Clicked() {
							go func() {
								_ = r.Store.RefreshVault(context.Background())
								_ = r.Store.RefreshSavedDownloads(context.Background())
								r.refreshExtensions()
								r.Store.TriggerRefresh()
							}()
						}
					})
					ErrorBanner(c, pal, errMsg)
				})

				ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
					ui.Segmented(c, &tab, "密码保险箱", "收藏夹", "浏览器扩展", "下载记录", "应用文件打开请求").Label("保险箱与文件管理分类")
				})
				st.mu.Lock()
				st.activeTab = tab
				st.mu.Unlock()

				switch tab {
				case 0:
					r.renderVaultTab(c, pal, snap, revealedID, revealedPW, busy)
				case 1:
					r.renderFavoritesTab(c, pal, snap, busy)
				case 2:
					r.renderExtensionsTab(c, pal, snap, exts, busy)
				case 3:
					r.renderDownloadsTab(c, pal, snap)
				case 4:
					r.renderDesktopFileRequestsTab(c, pal, snap)
				}
			})
	})
}

func (r *RootView) renderVaultTab(c *ui.Context, pal Palette, snap core.Snapshot, revealedID, revealedPW string, busy bool) {
	st := &r.browserMgmt
	Card(c, pal, func() {
		ui.Text(c, "新增网站凭据（加密保存在本机保险箱）").FontSize(14).Bold().TextColor(pal.Text)
		ui.Row(c).Gap(8).FillWidth().Children(func() {
			ui.TextInput(c, &st.vaultOrigin).Placeholder("网站 Origin（如 https://github.com）").Grow(1)
			ui.TextInput(c, &st.vaultUser).Placeholder("用户名 / 邮箱").Width(180)
			ui.TextInput(c, &st.vaultPass).Password().Placeholder("密码").Width(180)
			if ui.PrimaryButton(c, "保存凭据").Disabled(busy).Clicked() {
				go r.saveVaultEntry()
			}
		})
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "查看明文密码需输入 Airlock 账户密码：").FontSize(12).TextColor(pal.Muted)
			ui.TextInput(c, &st.revealAcctPW).Password().Placeholder("账户解锁密码").Width(200)
		})
	})

	Card(c, pal, func() {
		ui.Text(c, fmt.Sprintf("已保存凭据（%d 条）", len(snap.Vault.Entries))).FontSize(14).Bold().TextColor(pal.Text)
		if len(snap.Vault.Entries) == 0 {
			ui.Text(c, "暂无保存的网站账号").FontSize(12).TextColor(pal.Muted)
		}
		for _, entry := range snap.Vault.Entries {
			e := entry
			ui.Row(c).FillWidth().Background(pal.ElevatedBg).Border(1, pal.Border).Radius(6).Padding(8, 10).Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Gap(2).Children(func() {
					ui.Text(c, e.Origin).FontSize(13).Bold().TextColor(pal.Text)
					ui.Text(c, "账号："+e.Username).FontSize(11).TextColor(pal.Muted)
					if revealedID == e.ID && revealedPW != "" {
						ui.Text(c, "密码："+revealedPW).FontSize(12).Bold().TextColor(pal.Accent).Selectable()
					}
				})
				ui.Row(c).Gap(6).Children(func() {
					if ui.Button(c, "查看密码").Disabled(busy).Clicked() {
						go r.revealVaultEntry(e.ID)
					}
					if ui.Button(c, "复制密码").Disabled(busy).Clicked() {
						go r.copyVaultPassword(e.ID)
					}
					if ui.Button(c, "删除").Disabled(busy).Clicked() {
						go r.mutateVault(map[string]any{"action": "delete", "id": e.ID})
					}
				})
			})
		}
		if len(snap.Vault.Blocked) > 0 {
			ui.Divider(c)
			ui.Text(c, "已忽略保存提示的站点").FontSize(13).Bold().TextColor(pal.Text)
			for _, origin := range snap.Vault.Blocked {
				org := origin
				ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
					ui.Text(c, org).FontSize(12).TextColor(pal.Muted)
					if ui.Button(c, "取消忽略").Clicked() {
						go r.mutateVault(map[string]any{"action": "unblock", "origin": org})
					}
				})
			}
		}
	})
}

func (r *RootView) renderFavoritesTab(c *ui.Context, pal Palette, snap core.Snapshot, busy bool) {
	st := &r.browserMgmt
	Card(c, pal, func() {
		ui.Text(c, "浏览器新标签页与快捷收藏").FontSize(14).Bold().TextColor(pal.Text)
		ui.Row(c).Gap(8).FillWidth().Children(func() {
			ui.TextInput(c, &st.homeURL).Placeholder("https://www.bing.com/").Grow(1)
			if ui.Button(c, "保存主页地址").Disabled(busy).Clicked() {
				u := strings.TrimSpace(st.homeURL)
				go r.browserMgmtPost("/api/browser/preferences", map[string]string{"browser_home_url": u})
			}
		})
		ui.Divider(c)
		ui.Row(c).Gap(8).FillWidth().Children(func() {
			ui.TextInput(c, &st.favTitle).Placeholder("书签名称").Width(200)
			ui.TextInput(c, &st.favURL).Placeholder("https://...").Grow(1)
			if ui.PrimaryButton(c, "+ 添加收藏").Disabled(busy).Clicked() {
				title, u := strings.TrimSpace(st.favTitle), strings.TrimSpace(st.favURL)
				go func() {
					err := r.Client.PostJSON(context.Background(), "/api/favorites", map[string]any{
						"action": "save",
						"title":  title,
						"url":    u,
					}, nil)
					st.mu.Lock()
					if err != nil {
						st.errMsg = err.Error()
					} else {
						st.favTitle = ""
						st.favURL = ""
						st.errMsg = ""
					}
					st.mu.Unlock()
					r.Store.TriggerRefresh()
					r.Invalidate()
				}()
			}
		})
	})

	Card(c, pal, func() {
		ui.Text(c, fmt.Sprintf("书签列表（%d 个）", len(snap.Favorites.Items))).FontSize(14).Bold().TextColor(pal.Text)
		for _, fav := range snap.Favorites.Items {
			f := fav
			ui.Row(c).FillWidth().Background(pal.ElevatedBg).Border(1, pal.Border).Radius(6).Padding(8, 10).Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Gap(1).Children(func() {
					ui.Text(c, f.Title).FontSize(13).Bold().TextColor(pal.Text)
					ui.Text(c, f.URL).FontSize(11).TextColor(pal.Muted)
				})
				ui.Row(c).Gap(6).Children(func() {
					if ui.Button(c, "在浏览器打开").Disabled(!snap.Status.Ready).Clicked() {
						go func() {
							r.browserPost("/api/browser/new", map[string]any{"url": f.URL})
							r.SelectNav(NavBrowser)
						}()
					}
					if ui.Button(c, "删除").Clicked() {
						go r.browserMgmtPost("/api/favorites", map[string]any{"action": "delete", "id": f.ID})
					}
				})
			})
		}
	})
}

func (r *RootView) renderExtensionsTab(c *ui.Context, pal Palette, snap core.Snapshot, exts []browserExtensionItem, busy bool) {
	Card(c, pal, func() {
		ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "隔离浏览器扩展（ZIP 安装包）").FontSize(14).Bold().TextColor(pal.Text)
			ui.Row(c).Gap(8).Children(func() {
				if ui.PrimaryButton(c, "上传安装扩展 ZIP").Disabled(busy || !snap.Status.Ready).Clicked() {
					go r.uploadBrowserExtension()
				}
				if ui.Button(c, "刷新扩展").Disabled(!snap.Status.Ready).Clicked() {
					go r.refreshExtensions()
				}
			})
		})
		if len(exts) == 0 {
			ui.Text(c, "暂无已加载的第三方扩展，或环境未启动。").FontSize(12).TextColor(pal.Muted)
		}
		for _, ext := range exts {
			item := ext
			ui.Row(c).FillWidth().Background(pal.ElevatedBg).Border(1, pal.Border).Radius(6).Padding(8, 10).Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Gap(1).Children(func() {
					ui.Text(c, item.Name).FontSize(13).Bold().TextColor(pal.Text)
					ui.Text(c, item.ID+" · v"+item.Version).FontSize(11).TextColor(pal.Muted)
				})
				if ui.Button(c, "卸载").Clicked() {
					go func() {
						_ = r.Client.PostJSON(context.Background(), "/api/browser/extensions", map[string]any{
							"action": "remove",
							"id":     item.ID,
						}, nil)
						r.refreshExtensions()
					}()
				}
			})
		}
	})
}

func (r *RootView) renderDownloadsTab(c *ui.Context, pal Palette, snap core.Snapshot) {
	all := append([]core.BrowserDownload{}, snap.BrowserTabs.Downloads...)
	seen := map[string]bool{}
	for _, d := range all {
		seen[d.ID] = true
	}
	for _, d := range snap.SavedDownloads {
		if !seen[d.ID] {
			all = append(all, d)
		}
	}
	Card(c, pal, func() {
		ui.Text(c, fmt.Sprintf("浏览器与桌面下载（%d 项）", len(all))).FontSize(14).Bold().TextColor(pal.Text)
		if len(all) == 0 {
			ui.Text(c, "暂无下载文件记录").FontSize(12).TextColor(pal.Muted)
		}
		for _, dl := range all {
			d := dl
			ui.Row(c).FillWidth().Background(pal.ElevatedBg).Border(1, pal.Border).Radius(6).Padding(8, 10).Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Gap(1).Children(func() {
					ui.Text(c, d.Name).FontSize(13).Bold().TextColor(pal.Text)
					ui.Text(c, fmt.Sprintf("状态：%s · 大小：%d 字节", d.State, d.Total)).FontSize(11).TextColor(pal.Muted)
				})
				ui.Row(c).Gap(6).Children(func() {
					if ui.Button(c, "保存至本机").Clicked() {
						go r.handleBrowserFile("save", d.ID, d.Name)
					}
					if ui.Button(c, "在本机打开").Clicked() {
						go r.handleBrowserFile("open", d.ID, "")
					}
					if ui.Button(c, "在访达显示").Clicked() {
						go r.handleBrowserFile("reveal", d.ID, "")
					}
				})
			})
		}
	})
}

func (r *RootView) renderDesktopFileRequestsTab(c *ui.Context, pal Palette, snap core.Snapshot) {
	Card(c, pal, func() {
		ui.Text(c, "客机应用请求打开文件").FontSize(14).Bold().TextColor(pal.Text)
		if len(snap.DesktopFileRequests) == 0 {
			ui.Text(c, "暂无来自 Claude Desktop 等客机应用的待处理文件打开请求").FontSize(12).TextColor(pal.Muted)
		}
		for _, req := range snap.DesktopFileRequests {
			item := req
			ui.Row(c).FillWidth().Background(pal.ElevatedBg).Border(1, pal.Border).Radius(6).Padding(10).Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
				ui.Column(c).Gap(2).Children(func() {
					ui.Text(c, item.Name+" ("+item.Application+")").FontSize(13).Bold().TextColor(pal.Text)
					ui.Text(c, item.Path).FontSize(11).TextColor(pal.Muted)
				})
				if ui.PrimaryButton(c, "允许并在本机打开").Clicked() {
					go func() {
						if _, err := r.Client.RunFileCommand(context.Background(), "desktop-file", item.ID); err != nil {
							r.PushToast("打开文件失败：" + err.Error())
						} else {
							r.PushToast("已打开：" + item.Name)
						}
						r.Store.TriggerRefresh()
					}()
				}
			})
		}
	})
}

func (r *RootView) saveVaultEntry() {
	st := &r.browserMgmt
	st.mu.Lock()
	org, user, pw := strings.TrimSpace(st.vaultOrigin), strings.TrimSpace(st.vaultUser), st.vaultPass
	st.busy = true
	st.errMsg = ""
	st.mu.Unlock()
	r.Invalidate()

	err := r.Client.PostJSON(context.Background(), "/api/vault", map[string]any{
		"action": "save",
		"entry":  map[string]string{"origin": org, "username": user, "password": pw},
	}, nil)
	st.mu.Lock()
	st.busy = false
	if err != nil {
		st.errMsg = err.Error()
	} else {
		st.vaultOrigin = ""
		st.vaultUser = ""
		st.vaultPass = ""
	}
	st.mu.Unlock()
	_ = r.Store.RefreshVault(context.Background())
	r.Invalidate()
}

func (r *RootView) revealVaultEntry(id string) {
	st := &r.browserMgmt
	st.mu.Lock()
	pw := st.revealAcctPW
	st.busy = true
	st.errMsg = ""
	st.mu.Unlock()
	var resp struct {
		Password string `json:"password"`
	}
	err := r.Client.PostJSON(context.Background(), "/api/vault", map[string]any{
		"action":           "reveal",
		"id":               id,
		"account_password": pw,
	}, &resp)
	st.mu.Lock()
	st.busy = false
	if err != nil {
		st.errMsg = err.Error()
	} else {
		st.revealedID = id
		st.revealedPW = resp.Password
	}
	st.mu.Unlock()
	r.Invalidate()
}

func (r *RootView) copyVaultPassword(id string) {
	st := &r.browserMgmt
	st.mu.Lock()
	pw := st.revealAcctPW
	st.mu.Unlock()
	var resp struct {
		Password string `json:"password"`
	}
	err := r.Client.PostJSON(context.Background(), "/api/vault", map[string]any{
		"action":           "reveal",
		"id":               id,
		"account_password": pw,
	}, &resp)
	if err != nil {
		r.PushToast(err.Error())
		return
	}
	_ = macnative.PasteboardWriteConcealedText(resp.Password, 60)
	r.PushToast("密码已安全复制到剪贴板（60 秒后自动清除）")
}

func (r *RootView) mutateVault(body map[string]any) {
	if err := r.Client.PostJSON(context.Background(), "/api/vault", body, nil); err != nil {
		r.PushToast(err.Error())
	}
	_ = r.Store.RefreshVault(context.Background())
}

func (r *RootView) browserMgmtPost(path string, body any) {
	err := r.Client.PostJSON(context.Background(), path, body, nil)
	if err != nil {
		r.browserMgmt.mu.Lock()
		r.browserMgmt.errMsg = err.Error()
		r.browserMgmt.mu.Unlock()
	}
	r.Store.TriggerRefresh()
	r.Invalidate()
}

func (r *RootView) uploadBrowserExtension() {
	if r.Bridge == nil {
		return
	}
	paths, err := r.Bridge.OpenDialog(core.NativeDialogOptions{
		Title: "选择要上传的浏览器扩展 ZIP 包",
	})
	if err != nil || len(paths) == 0 {
		return
	}
	data, err := os.ReadFile(paths[0])
	if err != nil {
		r.PushToast("读取扩展文件失败：" + err.Error())
		return
	}
	if _, _, err := r.Client.DoRaw(context.Background(), "POST", "/api/browser/extensions/upload", data, "application/zip"); err != nil {
		r.PushToast("扩展上传失败：" + err.Error())
		return
	}
	r.PushToast("扩展已安装并激活")
	r.refreshExtensions()
}

func (r *RootView) handleBrowserFile(op, id, name string) {
	if op == "save" && r.Bridge != nil {
		dest, err := r.Bridge.SaveDialog(core.NativeDialogOptions{
			Title:       "保存下载文件到本机",
			DefaultPath: name,
		})
		if err != nil || dest == "" {
			return
		}
		if _, err := r.Client.RunFileCommand(context.Background(), "browser-file", "save", id, dest); err != nil {
			r.PushToast("保存失败：" + err.Error())
			return
		}
		r.PushToast("已保存到：" + dest)
		_ = r.Store.RefreshSavedDownloads(context.Background())
		return
	}
	if _, err := r.Client.RunFileCommand(context.Background(), "browser-file", op, id); err != nil {
		r.PushToast("操作失败：" + err.Error())
	}
}
