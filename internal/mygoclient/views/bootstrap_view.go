package views

import (
	"context"
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/sylvinus/agent-vm/internal/mygoclient/core"
)

func (r *RootView) renderBootLoading(c *ui.Context, pal Palette) {
	ui.Column(c).Fill().Background(pal.Bg).Children(func() {
		TitleBarClearance(c)
		ui.Column(c).FillWidth().Grow(1).Center().Gap(14).Children(func() {
			ui.Spinner(c)
			ui.Text(c, "正在启动 Cage 工作台…").FontSize(15).Bold().TextColor(pal.Text)
			ui.Text(c, "正在连接本地后台控制进程与加密存储").FontSize(12).TextColor(pal.Muted)
		})
	})
}

func (r *RootView) renderBootError(c *ui.Context, pal Palette, snap core.Snapshot) {
	ui.Column(c).Fill().Background(pal.Bg).Children(func() {
		TitleBarClearance(c)
		ui.Column(c).FillWidth().Grow(1).Center().Padding(24).Children(func() {
		ui.Box(c).MaxWidth(520).FillWidth().Children(func() {
			Card(c, pal, func() {
				SectionHeading(c, pal, "无法连接到 Cage 服务", "请确认后台控制进程和本地存储权限正常")
				ErrorBanner(c, pal, snap.BootError)
				ui.Row(c).Gap(10).Children(func() {
					if ui.PrimaryButton(c, "重试连接").Clicked() {
						go func() { _ = r.Store.BootstrapAndRefresh(context.Background()) }()
					}
					if ui.Button(c, "导出诊断包").Clicked() {
						r.OpenDiagnosticsExport()
					}
				})
			})
		})
		})
	})
}

type storageRecoveryState struct {
	mu      sync.Mutex
	busy    bool
	errMsg  string
	success string
}

var sharedStorageRecovery storageRecoveryState

func (r *RootView) renderStorageRecovery(c *ui.Context, pal Palette, snap core.Snapshot) {
	sharedStorageRecovery.mu.Lock()
	busy := sharedStorageRecovery.busy
	errMsg := sharedStorageRecovery.errMsg
	success := sharedStorageRecovery.success
	sharedStorageRecovery.mu.Unlock()

	rec := snap.Status.StorageRecovery
	ui.Column(c).Fill().Center().Background(pal.Bg).Padding(24).Children(func() {
		ui.Box(c).MaxWidth(560).FillWidth().Children(func() {
			Card(c, pal, func() {
				SectionHeading(c, pal, "需要恢复环境存储", "原 Cage 环境目录需要确认或重新挂载")
				ErrorBanner(c, pal, rec.Error)
				ErrorBanner(c, pal, errMsg)
				InfoBanner(c, pal, success)
				if snap.Status.Job.Running {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Spinner(c)
						ui.Text(c, snap.Status.Job.Message).FontSize(12).TextColor(pal.Text)
					})
				}
				ui.Row(c).Gap(10).Children(func() {
					if ui.PrimaryButton(c, "恢复环境存储").Disabled(busy || snap.Status.Job.Running).Clicked() {
						sharedStorageRecovery.mu.Lock()
						sharedStorageRecovery.busy = true
						sharedStorageRecovery.errMsg = ""
						sharedStorageRecovery.mu.Unlock()
						go func() {
							err := r.Client.PostJSON(context.Background(), "/api/environment/storage/recover", map[string]bool{"repair": rec.RepairAllowed}, nil)
							sharedStorageRecovery.mu.Lock()
							sharedStorageRecovery.busy = false
							if err != nil {
								sharedStorageRecovery.errMsg = err.Error()
							} else {
								sharedStorageRecovery.success = "已提交存储恢复请求"
							}
							sharedStorageRecovery.mu.Unlock()
							r.Store.TriggerRefresh()
							r.Invalidate()
						}()
					}
					if ui.Button(c, "导出诊断包").Clicked() {
						r.OpenDiagnosticsExport()
					}
				})
			})
		})
	})
}
