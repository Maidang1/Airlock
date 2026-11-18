//go:build darwin

package main

import (
	"context"

	"github.com/egoist/mygo"
	"github.com/sylvinus/agent-vm/internal/mygoclient/core"
	"github.com/sylvinus/agent-vm/internal/mygoclient/views"
)

func installMenus(rv *views.RootView, store *core.Store, bridge *mygoHostBridge, win *mygo.Window) {
	appName := "Cage"
	if store.Client().ClientOnly() {
		appName = "Cage Client"
	}
	menu := mygo.NewMenu([]*mygo.MenuItem{
		{
			Label: appName,
			Submenu: []*mygo.MenuItem{
				{Role: mygo.RoleAbout},
				mygo.Separator(),
				{
					Label: "检查应用更新…",
					Click: func(_ *mygo.MenuItem, _ *mygo.Window) {
						rv.SelectNav(views.NavSettings)
						go func() {
							st := bridge.HandleUpdate("check")
							store.SetUpdateState(st)
							_ = store.CheckUpdatesAbout(context.Background())
						}()
					},
				},
				{
					Label:       "偏好设置…",
					Accelerator: "CmdOrCtrl+,",
					Click: func(_ *mygo.MenuItem, _ *mygo.Window) {
						rv.SelectNav(views.NavSettings)
					},
				},
				mygo.Separator(),
				{Role: mygo.RoleHide},
				{Role: mygo.RoleHideOthers},
				{Role: mygo.RoleUnhide},
				mygo.Separator(),
				{Role: mygo.RoleQuit},
			},
		},
		{
			Label: "文件",
			Submenu: []*mygo.MenuItem{
				{
					Label:       "新建 Claude Code 终端",
					Accelerator: "CmdOrCtrl+T",
					Click: func(_ *mygo.MenuItem, _ *mygo.Window) {
						rv.CreateTerminal("claude")
					},
				},
				{
					Label:       "新建 Shell 终端",
					Accelerator: "CmdOrCtrl+Shift+T",
					Click: func(_ *mygo.MenuItem, _ *mygo.Window) {
						rv.CreateTerminal("shell")
					},
				},
				mygo.Separator(),
				{
					Label:       "关闭窗口",
					Accelerator: "CmdOrCtrl+W",
					Click: func(_ *mygo.MenuItem, w *mygo.Window) {
						if w != nil {
							w.Hide()
						}
					},
				},
			},
		},
		{
			Label: "编辑",
			Submenu: []*mygo.MenuItem{
				{Role: mygo.RoleUndo},
				{Role: mygo.RoleRedo},
				mygo.Separator(),
				{Role: mygo.RoleCut},
				{Role: mygo.RoleCopy},
				{Role: mygo.RolePaste},
				{Role: mygo.RoleSelectAll},
			},
		},
		{
			Label: "工作区",
			Submenu: []*mygo.MenuItem{
				{
					Label:       "终端与 Claude",
					Accelerator: "CmdOrCtrl+1",
					Click:       func(_ *mygo.MenuItem, _ *mygo.Window) { rv.SelectNav(views.NavTerminal) },
				},
				{
					Label:       "隔离浏览器",
					Accelerator: "CmdOrCtrl+2",
					Click:       func(_ *mygo.MenuItem, _ *mygo.Window) { rv.SelectNav(views.NavBrowser) },
				},
				{
					Label:       "Claude Desktop",
					Accelerator: "CmdOrCtrl+3",
					Click:       func(_ *mygo.MenuItem, _ *mygo.Window) { rv.SelectNav(views.NavClaudeDesktop) },
				},
				{
					Label:       "网络代理",
					Accelerator: "CmdOrCtrl+4",
					Click:       func(_ *mygo.MenuItem, _ *mygo.Window) { rv.SelectNav(views.NavProxy) },
				},
				{
					Label:       "安全检查",
					Accelerator: "CmdOrCtrl+5",
					Click:       func(_ *mygo.MenuItem, _ *mygo.Window) { rv.SelectNav(views.NavSafety) },
				},
				mygo.Separator(),
				{Role: mygo.RoleToggleFullScreen},
			},
		},
		{
			Label: "环境",
			Submenu: []*mygo.MenuItem{
				{
					Label: "启动隔离环境",
					Click: func(_ *mygo.MenuItem, _ *mygo.Window) {
						go func() {
							_ = store.Client().PostJSON(context.Background(), "/api/start", map[string]any{}, nil)
							store.TriggerRefresh()
						}()
					},
				},
				{
					Label: "停止隔离环境",
					Click: func(_ *mygo.MenuItem, _ *mygo.Window) {
						go func() {
							_ = store.Client().PostJSON(context.Background(), "/api/stop", map[string]any{}, nil)
							store.TriggerRefresh()
						}()
					},
				},
				{
					Label: "执行外网安全检查",
					Click: func(_ *mygo.MenuItem, _ *mygo.Window) {
						go func() {
							_ = store.Client().PostJSON(context.Background(), "/api/safety/check", map[string]any{}, nil)
							store.TriggerRefresh()
						}()
					},
				},
				mygo.Separator(),
				{
					Label: "刷新工作台状态",
					Accelerator: "CmdOrCtrl+R",
					Click: func(_ *mygo.MenuItem, _ *mygo.Window) {
						store.TriggerRefresh()
					},
				},
			},
		},
		{
			Label: "窗口",
			Submenu: []*mygo.MenuItem{
				{Role: mygo.RoleMinimize},
				{Role: mygo.RoleZoom},
				mygo.Separator(),
				{
					Label: "显示 Cage 主窗口",
					Click: func(_ *mygo.MenuItem, _ *mygo.Window) {
						win.Show()
						win.Focus()
					},
				},
				mygo.Separator(),
				{Role: mygo.RoleFront},
			},
		},
		{
			Label: "帮助",
			Submenu: []*mygo.MenuItem{
				{
					Label: "导出诊断包 (.zip)…",
					Click: func(_ *mygo.MenuItem, _ *mygo.Window) {
						rv.OpenDiagnosticsExport()
					},
				},
			},
		},
	})
	mygo.App.SetMenu(menu)
}
