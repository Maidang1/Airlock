package views

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/sylvinus/agent-vm/internal/macnative"
	"github.com/sylvinus/agent-vm/internal/mygoclient/core"
)

type cacheItemView struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Location string `json:"location"`
	Bytes    int64  `json:"bytes"`
	Files    int    `json:"files"`
	State    string `json:"state"`
	Reason   string `json:"reason"`
}

type cacheInventoryResp struct {
	PlanID  string          `json:"plan_id"`
	Notice  string          `json:"notice"`
	Items   []cacheItemView `json:"items"`
	Batches []struct {
		ID        string `json:"id"`
		CreatedAt int64  `json:"created_at"`
		Count     int    `json:"count"`
	} `json:"batches"`
}

type settingsViewState struct {
	mu           sync.Mutex
	activeTab    int // 0=General, 1=Security, 2=Resources&Maint, 3=Mobile, 4=Updates&Diagnostics
	synced       bool
	form         core.SettingsView
	oldPW        string
	newPW        string
	confirmPW    string
	genRK        string
	deviceStates map[string]string
	bioAvail     bool
	loadedDev    bool

	resourceCPUs int
	resourceMem  int
	resourceDisk int
	cpusStr      string
	memStr       string

	cacheInv  *cacheInventoryResp
	cacheSel  map[string]bool

	qrImage   *ui.Bitmap
	qrManual  string
	qrRelay   string

	busy   bool
	errMsg string
	notice string
}

func (r *RootView) ensureSettingsSynced(snap core.Snapshot) {
	st := &r.settings
	st.mu.Lock()
	if !st.synced && snap.Settings.Keyboard != "" {
		st.form = snap.Settings
		st.synced = true
	}
	if snap.Resources.CPUs > 0 && st.cpusStr == "" {
		st.resourceCPUs = snap.Resources.CPUs
		st.resourceMem = snap.Resources.MemoryGiB
		st.cpusStr = fmt.Sprintf("%d", snap.Resources.CPUs)
		st.memStr = fmt.Sprintf("%d", snap.Resources.MemoryGiB)
	}
	needDev := !st.loadedDev && snap.Account.DeviceBinding != ""
	if needDev {
		st.loadedDev = true
	}
	st.mu.Unlock()

	if needDev {
		res := r.Devices.Status(snap.Account.DeviceBinding, snap.Account.Devices)
		st.mu.Lock()
		st.bioAvail, _ = res["biometric_available"].(bool)
		if rawStates, ok := res["states"].(map[string]string); ok {
			st.deviceStates = rawStates
		}
		st.mu.Unlock()
	}
}

func (r *RootView) renderSettingsView(c *ui.Context, pal Palette, snap core.Snapshot) {
	r.ensureSettingsSynced(snap)
	st := &r.settings
	st.mu.Lock()
	tab := st.activeTab
	busy := st.busy
	errMsg := st.errMsg
	notice := st.notice
	bioAvail := st.bioAvail
	devStates := st.deviceStates
	inv := st.cacheInv
	qrBmp := st.qrImage
	qrManual := st.qrManual
	genRK := st.genRK
	st.mu.Unlock()

	PageScroll(c, pal, 980, 16, func() {
		ui.Column(c).Gap(14).FillWidth().Children(func() {
			Card(c, pal, func() {
				SectionHeading(c, pal, "设置、安全与维护", "配置偏好、生物识别、端口冲突检测、虚拟机资源分配、手机配对、更新与诊断")
				ErrorBanner(c, pal, errMsg)
				InfoBanner(c, pal, notice)
			})

			ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
				ui.Segmented(c, &tab, "常规与外观", "账户与快捷解锁", "资源、客机与缓存清理", "端口与冲突检测", "手机配对", "更新与诊断").Label("设置分类")
			})
			st.mu.Lock()
			st.activeTab = tab
			st.mu.Unlock()

			switch tab {
			case 0:
				r.renderGeneralSettingsTab(c, pal, snap, busy)
			case 1:
				r.renderSecuritySettingsTab(c, pal, snap, bioAvail, devStates, genRK, busy)
			case 2:
				r.renderResourcesAndCacheTab(c, pal, snap, inv, busy)
			case 3:
				r.renderPortConflictSettingsTab(c, pal, snap)
			case 4:
				r.renderMobilePairTab(c, pal, snap, qrBmp, qrManual, busy)
			case 5:
				r.renderUpdateAndDiagTab(c, pal, snap)
			}
		})
	})
}

func (r *RootView) renderGeneralSettingsTab(c *ui.Context, pal Palette, snap core.Snapshot, busy bool) {
	st := &r.settings
	prefs := snap.ClientPrefs

	Card(c, pal, func() {
		ui.Text(c, "本机客户端外观与声音").FontSize(14).Bold().TextColor(pal.Text)
		ui.Row(c).Gap(12).Wrap().AlignItems(ui.Center).Children(func() {
			themeMode := prefs.ThemeMode
			ui.Text(c, "主题模式：").FontSize(12).TextColor(pal.Muted)
			sel := ui.Select(c, &themeMode, []string{"system", "light", "dark"})
			if sel.Changed() {
				r.Store.UpdateClientPreferences(func(p *core.ClientPreferences) {
					p.ThemeMode = themeMode
				})
				SyncNativeAppTheme(themeMode)
			}

			muted := prefs.AudioMuted
			chk := ui.Checkbox(c, &muted, "静音客机音频输出")
			if chk.Changed() {
				r.Store.UpdateClientPreferences(func(p *core.ClientPreferences) {
					p.AudioMuted = muted
				})
			}
		})
	})

	Card(c, pal, func() {
		ui.Text(c, "环境与浏览器偏好设置").FontSize(14).Bold().TextColor(pal.Text)
		ui.Column(c).Gap(8).FillWidth().Children(func() {
			ui.Checkbox(c, &st.form.BalancedDisplay, "平衡显示缩放（Balanced Display）")
			ui.Checkbox(c, &st.form.ProjectFileCache, "开启项目文件缓存与共享剪贴板增强")
			ui.Checkbox(c, &st.form.Clock12Hour, "12 小时制时钟")
			ui.Checkbox(c, &st.form.AutoTimezone, "自动同步出口时区")
			ui.Checkbox(c, &st.form.AutoLanguage, "自动匹配出口语言")

			ui.Row(c).Gap(8).FillWidth().Children(func() {
				ui.Column(c).Gap(4).Grow(1).Children(func() {
					ui.Text(c, "新标签页网址").FontSize(11).TextColor(pal.Muted)
					ui.TextInput(c, &st.form.BrowserHomeURL).FillWidth()
				})
				ui.Column(c).Gap(4).Width(200).Children(func() {
					ui.Text(c, "Claude 启动命令").FontSize(11).TextColor(pal.Muted)
					ui.TextInput(c, &st.form.ClaudeTerminalCommand).FillWidth()
				})
				ui.Column(c).Gap(4).Width(120).Children(func() {
					ui.Text(c, "键盘映射").FontSize(11).TextColor(pal.Muted)
					ui.Select(c, &st.form.Keyboard, []string{"mac", "win"})
				})
			})

			ui.Row(c).Gap(8).FillWidth().Children(func() {
				ui.Column(c).Gap(4).Grow(1).Children(func() {
					ui.Text(c, "浏览器 DNS（DoH / IP）").FontSize(11).TextColor(pal.Muted)
					ui.TextInput(c, &st.form.DNSURL).FillWidth()
				})
				ui.Column(c).Gap(4).Width(180).Children(func() {
					ui.Text(c, "手动时区").FontSize(11).TextColor(pal.Muted)
					ui.TextInput(c, &st.form.Timezone).FillWidth()
				})
				ui.Column(c).Gap(4).Width(160).Children(func() {
					ui.Text(c, "区域语言 Locale").FontSize(11).TextColor(pal.Muted)
					ui.TextInput(c, &st.form.Locale).FillWidth()
				})
			})

			ui.Column(c).Gap(4).FillWidth().Children(func() {
				ui.Text(c, "自建手机中继服务器地址（留空使用官方中继）").FontSize(11).TextColor(pal.Muted)
				ui.TextInput(c, &st.form.MobileRelay).Placeholder("https://relay.example.com").FillWidth()
			})

			ui.Row(c).Gap(8).Children(func() {
				if ui.Button(c, "探测当前出口推荐配置").Disabled(busy || !snap.Status.VMRunning).Clicked() {
					go r.probeSettings()
				}
				if ui.PrimaryButton(c, "保存全部设置").Disabled(busy).Clicked() {
					go r.saveAllSettings()
				}
			})
		})
	})
}

func (r *RootView) renderSecuritySettingsTab(c *ui.Context, pal Palette, snap core.Snapshot, bioAvail bool, devStates map[string]string, genRK string, busy bool) {
	st := &r.settings
	acct := snap.Account

	Card(c, pal, func() {
		ui.Text(c, "快捷解锁与钥匙串绑定").FontSize(14).Bold().TextColor(pal.Text)
		if acct.TrustedLocal {
			ui.Text(c, "当前为免密本地模式，可在下方修改/设置密码以启用锁屏与生物识别").FontSize(12).TextColor(pal.Muted)
		} else {
			autoState := devStates["automatic"]
			bioState := devStates["biometric"]
			KeyValueRow(c, pal, "记住本机自动解锁", autoState)
			KeyValueRow(c, pal, "Touch ID 指纹解锁", bioState)

			ui.Row(c).Gap(8).Wrap().Children(func() {
				if acct.Devices["automatic"] == "" {
					if ui.Button(c, "开启记住本机解锁").Disabled(busy).Clicked() {
						go r.toggleDeviceEnroll("automatic", true, acct.DeviceBinding)
					}
				} else if ui.Button(c, "关闭记住本机解锁").Disabled(busy).Clicked() {
					go r.toggleDeviceEnroll("automatic", false, acct.DeviceBinding)
				}
				if bioAvail {
					if acct.Devices["biometric"] == "" {
						if ui.PrimaryButton(c, "开启 Touch ID 解锁").Disabled(busy).Clicked() {
							go r.toggleDeviceEnroll("biometric", true, acct.DeviceBinding)
						}
					} else if ui.Button(c, "关闭 Touch ID 解锁").Disabled(busy).Clicked() {
						go r.toggleDeviceEnroll("biometric", false, acct.DeviceBinding)
					}
				}
				if ui.Button(c, "立即锁定 Cage").Disabled(busy).Clicked() {
					go r.postSimple("/api/account/lock", map[string]any{})
				}
				if ui.Button(c, "退出登录并停止环境").Disabled(busy).Clicked() {
					go r.postSimple("/api/account/logout", map[string]any{})
				}
			})
		}
	})

	Card(c, pal, func() {
		ui.Text(c, "修改账户密码与生成恢复密钥").FontSize(14).Bold().TextColor(pal.Text)
		ui.Row(c).Gap(8).FillWidth().Children(func() {
			if !acct.TrustedLocal {
				ui.TextInput(c, &st.oldPW).Password().Placeholder("当前密码").Width(180)
			}
			ui.TextInput(c, &st.newPW).Password().Placeholder("新密码").Width(180)
			ui.TextInput(c, &st.confirmPW).Password().Placeholder("确认新密码").Width(180)
			if ui.PrimaryButton(c, "更新密码").Disabled(busy).Clicked() {
				go r.changeAccountPassword()
			}
		})
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			if ui.Button(c, "重新生成恢复密钥").Disabled(busy).Clicked() {
				go r.generateNewRecoveryKey()
			}
		})
		if genRK != "" {
			ui.Row(c).Background(pal.ElevatedBg).Border(1, pal.Border).Radius(6).Padding(8, 12).Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "新恢复密钥："+genRK).FontSize(12).Bold().TextColor(pal.Text).Selectable()
				if ui.Button(c, "复制").Clicked() {
					_ = macnative.PasteboardWriteText(genRK)
					r.PushToast("已复制新恢复密钥")
				}
			})
		}
	})
}

func (r *RootView) renderResourcesAndCacheTab(c *ui.Context, pal Palette, snap core.Snapshot, inv *cacheInventoryResp, busy bool) {
	st := &r.settings
	res := snap.Resources

	Card(c, pal, func() {
		ui.Text(c, "虚拟机硬件资源与环境组件").FontSize(14).Bold().TextColor(pal.Text)
		KeyValueRow(c, pal, "当前分配", fmt.Sprintf("%d 核 CPU · %d GiB 内存 (上限 %d 核 / %d GiB)", res.CPUs, res.MemoryGiB, res.MaxCPUs, res.MaxMemoryGiB))
		ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "CPU 核数：").FontSize(12).TextColor(pal.Muted)
			ui.TextInput(c, &st.cpusStr).Width(70)
			ui.Text(c, "内存 GiB：").FontSize(12).TextColor(pal.Muted)
			ui.TextInput(c, &st.memStr).Width(70)
			if ui.PrimaryButton(c, "应用资源配额").Disabled(busy).Clicked() {
				go r.applyVMResources()
			}
		})
		ui.Divider(c)
		ui.Row(c).Gap(8).Wrap().Children(func() {
			if ui.Button(c, "更新环境组件与工具").Disabled(busy || snap.Status.Job.Running).Clicked() {
				go r.postSimple("/api/environment/tools", map[string]any{})
			}
			if ui.Button(c, "更新基础系统镜像").Disabled(busy || snap.Status.Job.Running).Clicked() {
				go r.postSimple("/api/environment/update", map[string]any{})
			}
			if snap.Status.GuestSystem == "macos" {
				if ui.Button(c, "打开 macOS 屏幕共享窗口").Clicked() {
					go r.postSimple("/api/environment/macos-screen-sharing", map[string]any{})
				}
				if ui.Button(c, "维护客机屏幕权限").Clicked() {
					r.OpenMaintenanceModal("screen")
				}
				if ui.Button(c, "维护客机输入辅助功能").Clicked() {
					r.OpenMaintenanceModal("input")
				}
			}
		})
	})

	Card(c, pal, func() {
		ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "环境磁盘缓存盘点与安全清理").FontSize(14).Bold().TextColor(pal.Text)
			if ui.Button(c, "盘点可清理缓存").Disabled(busy || !snap.Status.Ready).Clicked() {
				go r.inspectCacheInventory()
			}
		})
		if inv != nil {
			if inv.Notice != "" {
				ui.Text(c, inv.Notice).FontSize(12).TextColor(pal.Muted)
			}
			for _, item := range inv.Items {
				it := item
				st.mu.Lock()
				if st.cacheSel == nil {
					st.cacheSel = map[string]bool{}
				}
				checked := st.cacheSel[it.ID]
				st.mu.Unlock()
				ui.Row(c).FillWidth().Background(pal.ElevatedBg).Border(1, pal.Border).Radius(6).Padding(8, 10).Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
					cb := ui.Checkbox(c, &checked, fmt.Sprintf("%s (%s · %.1f MB)", it.Title, it.Location, float64(it.Bytes)/(1024*1024))).Disabled(it.State != "ready")
					if cb.Changed() {
						st.mu.Lock()
						st.cacheSel[it.ID] = checked
						st.mu.Unlock()
					}
					ui.Text(c, it.State+" "+it.Reason).FontSize(11).TextColor(pal.Muted)
				})
			}
			ui.Row(c).Gap(8).Children(func() {
				if ui.PrimaryButton(c, "清理选中缓存").Disabled(busy).Clicked() {
					go r.cleanSelectedCaches(inv.PlanID)
				}
				for _, b := range inv.Batches {
					batch := b
					if ui.Button(c, fmt.Sprintf("回滚批次 %s (%d项)", batch.ID[:min(8, len(batch.ID))], batch.Count)).Clicked() {
						go r.restoreCacheBatch(batch.ID)
					}
				}
			})
		}
	})
}

func (r *RootView) renderMobilePairTab(c *ui.Context, pal Palette, snap core.Snapshot, qrBmp *ui.Bitmap, qrManual string, busy bool) {
	mp := snap.MobilePair
	Card(c, pal, func() {
		ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
			SectionHeading(c, pal, "手机端加密远程控制配对", "通过端到端加密中继在手机浏览器查看、批复终端及控制桌面")
			if mp.Paired {
				PillBadge(c, "已配对手机", pal.Success, pal.SuccessSoft)
			} else {
				PillBadge(c, "未配对", pal.Muted, pal.ElevatedBg)
			}
		})
		if mp.Status != "" {
			KeyValueRow(c, pal, "当前状态", mp.Status)
		}
		if mp.Device != nil {
			KeyValueRow(c, pal, "已绑定设备", mp.Device.Model+" ("+mp.Device.Platform+")")
		}

		ui.Row(c).Gap(8).Children(func() {
			if !mp.Paired {
				if ui.PrimaryButton(c, "生成配对二维码").Disabled(busy).Clicked() {
					go r.startMobilePairing(snap.Settings.MobileRelay)
				}
			} else {
				if ui.Button(c, "解除手机配对").Disabled(busy).Clicked() {
					go r.removeMobilePair()
				}
			}
		})

		if qrBmp != nil {
			ui.Row(c).Gap(16).AlignItems(ui.Center).Children(func() {
				ui.Image(c, qrBmp).Size(200, 200)
				ui.Column(c).Gap(8).Grow(1).Children(func() {
					ui.Text(c, "请使用手机相机或微信扫一扫打开配对页，有效期 2 分钟").FontSize(13).Bold().TextColor(pal.Text)
					if qrManual != "" {
						ui.Text(c, "或手动输入配对口令：").FontSize(12).TextColor(pal.Muted)
						ui.Text(c, qrManual).FontSize(11).TextColor(pal.Text).Selectable()
						if ui.Button(c, "复制口令").Clicked() {
							_ = macnative.PasteboardWriteText(qrManual)
							r.PushToast("已复制手机配对口令")
						}
					}
				})
			})
		}
	})
}

func (r *RootView) renderUpdateAndDiagTab(c *ui.Context, pal Palette, snap core.Snapshot) {
	phase, _ := snap.Update["phase"].(string)
	if phase == "" {
		phase = "idle"
	}
	curVer, _ := snap.Update["currentVersion"].(string)
	if curVer == "" {
		curVer = snap.Build.Version
	}
	msg, _ := snap.Update["message"].(string)

	Card(c, pal, func() {
		ui.Text(c, "应用版本与更新").FontSize(14).Bold().TextColor(pal.Text)
		KeyValueRow(c, pal, "当前版本", curVer+" ("+snap.Build.Commit+")")
		KeyValueRow(c, pal, "更新器状态", phase)
		if msg != "" {
			ui.Text(c, msg).FontSize(12).TextColor(pal.Text)
		}
		ui.Row(c).Gap(8).Wrap().Children(func() {
			if ui.PrimaryButton(c, "检查应用更新").Clicked() && r.Bridge != nil {
				go func() {
					st := r.Bridge.HandleUpdate("check")
					r.Store.SetUpdateState(st)
					_ = r.Store.CheckUpdatesAbout(context.Background())
				}()
			}
			if phase == "available" || phase == "ready" || phase == "failed" {
				if ui.Button(c, "下载并安装更新").Clicked() && r.Bridge != nil {
					go func() {
						st := r.Bridge.HandleUpdate("install")
						r.Store.SetUpdateState(st)
					}()
				}
			}
			if phase == "downloading" || phase == "verifying" {
				if ui.Button(c, "取消更新").Clicked() && r.Bridge != nil {
					st := r.Bridge.HandleUpdate("cancel")
					r.Store.SetUpdateState(st)
				}
			}
		})
	})

	Card(c, pal, func() {
		ui.Text(c, "运行诊断日志").FontSize(14).Bold().TextColor(pal.Text)
		KeyValueRow(c, pal, "诊断日志大小", fmt.Sprintf("%d 个文件 · %.1f KB", snap.Diagnostics.Files, float64(snap.Diagnostics.Bytes)/1024))
		if ui.PrimaryButton(c, "导出诊断包 (.zip)").Clicked() {
			r.OpenDiagnosticsExport()
		}
	})
}

func (r *RootView) probeSettings() {
	st := &r.settings
	st.mu.Lock()
	form := st.form
	st.busy = true
	st.errMsg = ""
	st.mu.Unlock()
	var probe struct {
		Timezone string `json:"timezone"`
		Locale   string `json:"locale"`
	}
	err := r.Client.PostJSON(context.Background(), "/api/settings/probe", form, &probe)
	st.mu.Lock()
	st.busy = false
	if err != nil {
		st.errMsg = err.Error()
	} else {
		if probe.Timezone != "" {
			st.form.Timezone = probe.Timezone
		}
		if probe.Locale != "" {
			st.form.Locale = probe.Locale
		}
		st.notice = "已探测出口推荐时区与语言设置"
	}
	st.mu.Unlock()
	r.Invalidate()
}

func (r *RootView) saveAllSettings() {
	st := &r.settings
	st.mu.Lock()
	form := st.form
	st.busy = true
	st.errMsg = ""
	st.mu.Unlock()
	err := r.Client.PostJSON(context.Background(), "/api/settings", form, nil)
	st.mu.Lock()
	st.busy = false
	if err != nil {
		st.errMsg = err.Error()
	} else {
		st.notice = "设置已保存"
	}
	st.mu.Unlock()
	r.Store.TriggerRefresh()
	r.Invalidate()
}

func (r *RootView) toggleDeviceEnroll(kind string, enable bool, binding string) {
	st := &r.settings
	st.mu.Lock()
	st.busy = true
	st.errMsg = ""
	st.mu.Unlock()

	var states map[string]any
	var err error
	if enable {
		states, err = r.Devices.Enroll(context.Background(), kind, binding)
	} else {
		states, err = r.Devices.Revoke(context.Background(), kind, binding)
	}
	st.mu.Lock()
	st.busy = false
	if err != nil {
		st.errMsg = err.Error()
	} else if sMap, ok := states["states"].(map[string]string); ok {
		st.deviceStates = sMap
	}
	st.mu.Unlock()
	r.Store.TriggerRefresh()
	r.Invalidate()
}

func (r *RootView) changeAccountPassword() {
	st := &r.settings
	st.mu.Lock()
	oldP, newP, confP := st.oldPW, st.newPW, st.confirmPW
	st.busy = true
	st.errMsg = ""
	st.mu.Unlock()
	err := r.Client.PostJSON(context.Background(), "/api/account/password", map[string]string{
		"old_password":     oldP,
		"password":         newP,
		"confirm_password": confP,
	}, nil)
	st.mu.Lock()
	st.busy = false
	if err != nil {
		st.errMsg = err.Error()
	} else {
		st.oldPW, st.newPW, st.confirmPW = "", "", ""
		st.notice = "账户密码已更新"
	}
	st.mu.Unlock()
	r.Store.TriggerRefresh()
	r.Invalidate()
}

func (r *RootView) generateNewRecoveryKey() {
	st := &r.settings
	st.mu.Lock()
	pw := st.oldPW
	st.busy = true
	st.errMsg = ""
	st.mu.Unlock()
	var resp core.AccountView
	err := r.Client.PostJSON(context.Background(), "/api/account/recovery-generate", map[string]string{
		"password": pw,
	}, &resp)
	st.mu.Lock()
	st.busy = false
	if err != nil {
		st.errMsg = err.Error()
	} else {
		st.genRK = resp.RecoveryKey
	}
	st.mu.Unlock()
	r.Invalidate()
}

func (r *RootView) applyVMResources() {
	st := &r.settings
	st.mu.Lock()
	cpus, _ := strconv.Atoi(strings.TrimSpace(st.cpusStr))
	mem, _ := strconv.Atoi(strings.TrimSpace(st.memStr))
	st.busy = true
	st.errMsg = ""
	st.mu.Unlock()
	err := r.Client.PostJSON(context.Background(), "/api/environment/resources", map[string]any{
		"automatic":  false,
		"cpus":       cpus,
		"memory_gib": mem,
	}, nil)
	st.mu.Lock()
	st.busy = false
	if err != nil {
		st.errMsg = err.Error()
	} else {
		st.notice = "资源配额已更新"
	}
	st.mu.Unlock()
	r.Store.TriggerRefresh()
	r.Invalidate()
}

func (r *RootView) inspectCacheInventory() {
	var inv cacheInventoryResp
	err := r.Client.PostJSON(context.Background(), "/api/environment/cache/inventory", map[string]any{}, &inv)
	r.settings.mu.Lock()
	if err != nil {
		r.settings.errMsg = err.Error()
	} else {
		r.settings.cacheInv = &inv
		r.settings.cacheSel = map[string]bool{}
		for _, it := range inv.Items {
			if it.State == "ready" {
				r.settings.cacheSel[it.ID] = true
			}
		}
	}
	r.settings.mu.Unlock()
	r.Invalidate()
}

func (r *RootView) cleanSelectedCaches(planID string) {
	r.settings.mu.Lock()
	var items []string
	for id, ok := range r.settings.cacheSel {
		if ok {
			items = append(items, id)
		}
	}
	r.settings.busy = true
	r.settings.errMsg = ""
	r.settings.mu.Unlock()
	err := r.Client.PostJSON(context.Background(), "/api/environment/cache/clean", map[string]any{
		"plan_id": planID,
		"items":   items,
	}, nil)
	r.settings.mu.Lock()
	r.settings.busy = false
	if err != nil {
		r.settings.errMsg = err.Error()
	} else {
		r.settings.notice = "选中缓存已安全清理"
	}
	r.settings.mu.Unlock()
	r.inspectCacheInventory()
}

func (r *RootView) restoreCacheBatch(batchID string) {
	err := r.Client.PostJSON(context.Background(), "/api/environment/cache/restore", map[string]string{
		"batch": batchID,
	}, nil)
	if err != nil {
		r.PushToast("回滚失败：" + err.Error())
		return
	}
	r.PushToast("缓存已恢复")
	r.inspectCacheInventory()
}

func (r *RootView) startMobilePairing(relay string) {
	var resp struct {
		Image  string `json:"image"`
		Manual string `json:"manual"`
	}
	st := &r.settings
	st.mu.Lock()
	st.busy = true
	st.errMsg = ""
	st.mu.Unlock()

	body := map[string]string{}
	if relay != "" {
		body["relay"] = relay
	}
	err := r.Client.PostJSON(context.Background(), "/api/mobile-pair", body, &resp)
	var bmp *ui.Bitmap
	if err == nil && resp.Image != "" {
		b64 := strings.TrimPrefix(resp.Image, "data:image/png;base64,")
		if pngBytes, decErr := base64.StdEncoding.DecodeString(b64); decErr == nil {
			bmp, _ = ui.DecodeBitmap(pngBytes)
		}
	}
	st.mu.Lock()
	st.busy = false
	if err != nil {
		st.errMsg = err.Error()
	} else {
		st.qrImage = bmp
		st.qrManual = resp.Manual
	}
	st.mu.Unlock()
	r.Store.TriggerRefresh()
	r.Invalidate()
}

func (r *RootView) removeMobilePair() {
	if err := r.Client.DeleteJSON(context.Background(), "/api/mobile-pair", nil); err != nil {
		r.PushToast(err.Error())
		return
	}
	r.settings.mu.Lock()
	r.settings.qrImage = nil
	r.settings.qrManual = ""
	r.settings.mu.Unlock()
	r.Store.TriggerRefresh()
}
