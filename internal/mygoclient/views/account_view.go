package views

import (
	"context"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/sylvinus/agent-vm/internal/macnative"
	"github.com/sylvinus/agent-vm/internal/mygoclient/core"
)

type accountState struct {
	mu                     sync.Mutex
	username               string
	password               string
	confirmPassword        string
	recoveryKeyInput       string
	pendingRecoveryKey     string
	recoveryConfirmed      bool
	showRecoveryMode       bool
	rememberLogin          bool
	rememberLoginInit      bool
	trustedResumeTriedConn string
	busy                   bool
	errMsg                 string
	notice                 string
	autoUnlockTried        bool
}

func isLocalDesktopGateway(ctx core.ClientContext) bool {
	return ctx.DesktopGateway && ctx.Mode == "local"
}

func (r *RootView) renderAccountGate(c *ui.Context, pal Palette, snap core.Snapshot) {
	st := &r.account
	acct := snap.Account
	localDesktop := isLocalDesktopGateway(snap.Context)
	connID := snap.Context.ConnectionID
	if connID == "" {
		connID = "_local"
	}

	// Initialize default "remember me" state once while locked, and automatically
	// resume local trusted login or legacy Keychain unlock when allowed.
	st.mu.Lock()
	if !st.rememberLoginInit && !acct.Unlocked {
		st.rememberLogin = !acct.Exists || acct.TrustedLogin || acct.Devices["automatic"] != ""
		st.rememberLoginInit = true
	}
	shouldTrustedResume := acct.Exists && !acct.Unlocked && !acct.RequireAuthentication &&
		localDesktop && (acct.TrustedLocal || acct.TrustedLogin) &&
		st.trustedResumeTriedConn != connID && !st.busy
	if shouldTrustedResume {
		st.trustedResumeTriedConn = connID
		st.busy = true
	}
	shouldAuto := !shouldTrustedResume && acct.Exists && !acct.Unlocked && !acct.TrustedLocal &&
		!acct.RequireAuthentication && !(acct.TrustedLogin && localDesktop) &&
		acct.Devices["automatic"] != "" && !st.autoUnlockTried && !st.busy
	if shouldAuto {
		st.autoUnlockTried = true
		st.busy = true
	}
	st.mu.Unlock()
	if shouldTrustedResume {
		go r.resumeTrustedLogin(snap.Context.ConnectionID)
	} else if shouldAuto {
		go r.unlockWithDevice(acct.DeviceBinding, "automatic", "")
	}

	ui.Column(c).Fill().Background(pal.Bg).Children(func() {
		TitleBarClearance(c)
		ui.Column(c).FillWidth().Grow(1).Center().Padding(24).Children(func() {
			ui.Box(c).MaxWidth(460).FillWidth().Children(func() {
			Card(c, pal, func() {
				st.mu.Lock()
				pendingRK := st.pendingRecoveryKey
				showReset := st.showRecoveryMode
				busy := st.busy
				errMsg := st.errMsg
				notice := st.notice
				st.mu.Unlock()

				if pendingRK != "" {
					SectionHeading(c, pal, "请妥善保存恢复密钥", "忘记账户密码或重置环境时需要此密钥，系统不会再次明文显示")
					InfoBanner(c, pal, notice)
					ui.Box(c).Background(pal.ElevatedBg).Border(1, pal.Border).Radius(8).Padding(12).Children(func() {
						ui.Text(c, pendingRK).FontSize(14).Bold().TextColor(pal.Text).Selectable()
					})
					if ui.Button(c, "复制恢复密钥").Clicked() {
						_ = macnative.PasteboardWriteText(pendingRK)
						r.PushToast("恢复密钥已复制到剪贴板")
					}
					ui.Checkbox(c, &st.recoveryConfirmed, "我已安全保存该恢复密钥")
					if ui.PrimaryButton(c, "继续进入 Cage").Disabled(!st.recoveryConfirmed).Clicked() {
						st.mu.Lock()
						st.pendingRecoveryKey = ""
						st.recoveryConfirmed = false
						st.notice = ""
						st.mu.Unlock()
						r.Store.TriggerRefresh()
					}
					return
				}

				if !acct.Exists {
					SectionHeading(c, pal, "创建本机 Cage 安全账户", "用于保护客机登录凭证、密码保险箱及多设备配对权限")
					ErrorBanner(c, pal, errMsg)
					ui.Column(c).Gap(8).Children(func() {
						ui.Text(c, "用户名").FontSize(12).TextColor(pal.Muted)
						ui.TextInput(c, &st.username).Label("用户名").Placeholder("输入用户名（例如 cage）").FillWidth()
						if acct.MacOSAccountHost {
							ui.Text(c, "提示：与 macOS 客机共用时，用户名需以小写字母开头（仅限小写字母、数字或下划线），密码需为 8–256 位英文字符").FontSize(11).TextColor(pal.Muted)
						}
						ui.Text(c, "密码").FontSize(12).TextColor(pal.Muted)
						ui.TextInput(c, &st.password).Label("密码").Password().Placeholder("设置解锁密码").FillWidth()
						ui.Text(c, "确认密码").FontSize(12).TextColor(pal.Muted)
						inp := ui.TextInput(c, &st.confirmPassword).Label("确认密码").Password().Placeholder("再次输入密码").FillWidth()
						if inp.Submitted() && !busy {
							go r.submitAccountCreate()
						}
					})
					if localDesktop {
						ui.Checkbox(c, &st.rememberLogin, "在此设备记住登录状态")
					}
					ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
						if ui.PrimaryButton(c, "创建账户").Disabled(busy).Clicked() {
							go r.submitAccountCreate()
						}
						if ui.Button(c, "导出诊断包").Clicked() {
							r.OpenDiagnosticsExport()
						}
					})
					return
				}

				if showReset {
					SectionHeading(c, pal, "使用恢复密钥重设密码", "重设后将生成新的恢复密钥")
					ErrorBanner(c, pal, errMsg)
					ui.Column(c).Gap(8).Children(func() {
						ui.Text(c, "恢复密钥").FontSize(12).TextColor(pal.Muted)
						ui.TextInput(c, &st.recoveryKeyInput).Placeholder("输入备份的恢复密钥").FillWidth()
						ui.Text(c, "新密码").FontSize(12).TextColor(pal.Muted)
						ui.TextInput(c, &st.password).Password().Placeholder("输入新密码").FillWidth()
						ui.Text(c, "确认新密码").FontSize(12).TextColor(pal.Muted)
						ui.TextInput(c, &st.confirmPassword).Password().Placeholder("再次输入新密码").FillWidth()
					})
					ui.Row(c).Gap(10).Children(func() {
						if ui.PrimaryButton(c, "重设密码").Disabled(busy).Clicked() {
							go r.submitRecoveryReset()
						}
						if ui.Button(c, "返回密码解锁").Clicked() {
							st.mu.Lock()
							st.showRecoveryMode = false
							st.errMsg = ""
							st.mu.Unlock()
						}
					})
					return
				}

				// Locked / Unlock view
				name := acct.Username
				if name == "" {
					name = "Cage 用户"
				}
				SectionHeading(c, pal, "解锁 Cage · "+name, "验证身份后访问环境、保险箱与终端会话")
				if acct.BackgroundRunning {
					InfoBanner(c, pal, "后台虚拟机与定时任务正在运行中")
				}
				ErrorBanner(c, pal, errMsg)
				InfoBanner(c, pal, notice)
				if acct.TrustedLocal {
					trustedDesc := "请回到受信任的桌面设置账户密码，再从此处登录"
					if localDesktop {
						trustedDesc = "使用当前系统用户的本机信任凭据进入工作区"
					}
					ui.Text(c, trustedDesc).FontSize(12).TextColor(pal.Muted)
					ui.Row(c).Gap(8).Wrap().Children(func() {
						if localDesktop {
							if ui.PrimaryButton(c, "重新进入本机工作区").Disabled(busy).Clicked() {
								go r.resumeTrustedLogin(snap.Context.ConnectionID)
							}
						}
						if ui.Button(c, "导出诊断包").Clicked() {
							r.OpenDiagnosticsExport()
						}
					})
					return
				}
				ui.Column(c).Gap(8).Children(func() {
					ui.Text(c, "密码").FontSize(12).TextColor(pal.Muted)
					inp := ui.TextInput(c, &st.password).Label("解锁密码").Password().Placeholder("输入账户密码").AutoFocus().FillWidth()
					if inp.Submitted() && !busy {
						go r.submitAccountUnlock()
					}
				})
				if localDesktop {
					ui.Checkbox(c, &st.rememberLogin, "在此设备记住登录状态")
				}
				ui.Row(c).Gap(8).Wrap().Children(func() {
					if ui.PrimaryButton(c, "解锁").Disabled(busy).Clicked() {
						go r.submitAccountUnlock()
					}
					if acct.Devices["biometric"] != "" && macnative.BiometricAvailable() {
						if ui.Button(c, "使用 Touch ID 解锁").Disabled(busy).Clicked() {
							go r.unlockWithDevice(acct.DeviceBinding, "biometric", "使用 Touch ID 解锁 Cage")
						}
					}
					if acct.Devices["automatic"] != "" {
						if ui.Button(c, "使用本机钥匙串解锁").Disabled(busy).Clicked() {
							go r.unlockWithDevice(acct.DeviceBinding, "automatic", "从钥匙串读取解锁凭据")
						}
					}
				})
				ui.Row(c).Gap(10).Children(func() {
					if ui.Button(c, "忘记密码？使用恢复密钥").Clicked() {
						st.mu.Lock()
						st.showRecoveryMode = true
						st.errMsg = ""
						st.mu.Unlock()
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

func (r *RootView) renderMacOSCredentialsGate(c *ui.Context, pal Palette, snap core.Snapshot) {
	st := &r.account
	acct := snap.Account
	defaultUser := acct.MacOSGuestUser
	if defaultUser == "" {
		defaultUser = acct.Username
	}
	if defaultUser == "" {
		defaultUser = "cage"
	}
	st.mu.Lock()
	if st.username == "" {
		st.username = defaultUser
	}
	busy := st.busy
	errMsg := st.errMsg
	notice := st.notice
	st.mu.Unlock()

	subtitle := "输入当前 Cage 密码，确认用于 macOS 客机（小写字母开头的用户名，8–256 位英文字符密码）"
	if acct.TrustedLocal {
		subtitle = "先设置 Cage 与 macOS 客机共用账密（小写字母开头的用户名，8–256 位英文字符密码），再继续安装"
	}

	ui.Column(c).Fill().Background(pal.Bg).Children(func() {
		TitleBarClearance(c)
		ui.Column(c).FillWidth().Grow(1).Center().Padding(24).Children(func() {
			ui.Box(c).MaxWidth(460).FillWidth().Children(func() {
			Card(c, pal, func() {
				SectionHeading(c, pal, "设置 macOS 客机共用账密", subtitle)
				ErrorBanner(c, pal, errMsg)
				InfoBanner(c, pal, notice)
				ui.Column(c).Gap(8).Children(func() {
					ui.Text(c, "客机用户名").FontSize(12).TextColor(pal.Muted)
					ui.TextInput(c, &st.username).Label("客机用户名").Placeholder("macOS 客机用户名").Disabled(acct.MacOSUsernameBound || acct.MacOSGuestUser != "").FillWidth()
					ui.Text(c, "设置密码").FontSize(12).TextColor(pal.Muted)
					ui.TextInput(c, &st.password).Label("设置密码").Password().Placeholder("输入密码").FillWidth()
					ui.Text(c, "确认密码").FontSize(12).TextColor(pal.Muted)
					inp := ui.TextInput(c, &st.confirmPassword).Label("确认密码").Password().Placeholder("再次确认密码").FillWidth()
					if inp.Submitted() && !busy {
						go r.submitPrepareMacOS()
					}
				})
				ui.Row(c).Gap(10).Children(func() {
					if ui.PrimaryButton(c, "保存并准备环境").Disabled(busy).Clicked() {
						go r.submitPrepareMacOS()
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

func (r *RootView) applyTrustedLogin(action, password, connID string) error {
	payload := map[string]string{
		"action":        action,
		"connection_id": connID,
	}
	if password != "" {
		payload["password"] = password
	}
	return r.Client.PostJSON(context.Background(), "/api/client/trusted-login", payload, nil)
}

func (r *RootView) resumeTrustedLogin(connID string) {
	st := &r.account
	st.mu.Lock()
	st.busy = true
	st.errMsg = ""
	st.notice = ""
	st.mu.Unlock()
	r.Invalidate()

	err := r.applyTrustedLogin("resume", "", connID)
	if err == nil {
		r.Store.RefreshAll(context.Background())
	}
	st.mu.Lock()
	st.busy = false
	if err != nil {
		st.errMsg = err.Error()
	} else {
		st.rememberLoginInit = false
	}
	st.mu.Unlock()
	r.Invalidate()
}

func (r *RootView) submitAccountCreate() {
	st := &r.account
	st.mu.Lock()
	user := strings.TrimSpace(st.username)
	pw, confirm := st.password, st.confirmPassword
	remember := st.rememberLogin
	st.busy = true
	st.errMsg = ""
	st.notice = ""
	st.mu.Unlock()
	r.Invalidate()

	var resp core.AccountView
	err := r.Client.PostJSON(context.Background(), "/api/account/create", map[string]string{
		"username":         user,
		"password":         pw,
		"confirm_password": confirm,
	}, &resp)
	var trustErr error
	snapCtx := r.Store.Snapshot().Context
	if err == nil && isLocalDesktopGateway(snapCtx) {
		action := "disable"
		if remember {
			action = "enable"
		}
		trustErr = r.applyTrustedLogin(action, pw, snapCtx.ConnectionID)
	}
	if err == nil {
		r.Store.RefreshAll(context.Background())
	}
	st.mu.Lock()
	st.busy = false
	if err != nil {
		st.errMsg = err.Error()
	} else {
		st.password = ""
		st.confirmPassword = ""
		st.pendingRecoveryKey = resp.RecoveryKey
		st.recoveryConfirmed = false
		st.rememberLoginInit = false
		st.trustedResumeTriedConn = ""
		if trustErr != nil {
			if remember {
				st.notice = "账户已创建，记住登录未能开启：" + trustErr.Error() + "。可在「设置 → 账户」中重试。"
			} else {
				st.notice = "账户已创建，记住登录未能关闭：" + trustErr.Error() + "。可在「设置 → 账户」中重试。"
			}
		}
	}
	st.mu.Unlock()
	r.Invalidate()
}

func (r *RootView) submitPrepareMacOS() {
	st := &r.account
	st.mu.Lock()
	user := strings.TrimSpace(st.username)
	pw, confirm := st.password, st.confirmPassword
	st.busy = true
	st.errMsg = ""
	st.mu.Unlock()
	r.Invalidate()

	err := r.Client.PostJSON(context.Background(), "/api/account/prepare-macos", map[string]string{
		"username":         user,
		"password":         pw,
		"confirm_password": confirm,
	}, nil)
	if err == nil {
		r.Store.RefreshAll(context.Background())
	}
	st.mu.Lock()
	st.busy = false
	if err != nil {
		st.errMsg = err.Error()
	} else {
		st.password = ""
		st.confirmPassword = ""
		st.notice = "macOS 客机共用账密已就绪"
	}
	st.mu.Unlock()
	r.Invalidate()
}

func (r *RootView) submitAccountUnlock() {
	st := &r.account
	st.mu.Lock()
	pw := st.password
	remember := st.rememberLogin
	st.busy = true
	st.errMsg = ""
	st.notice = ""
	st.mu.Unlock()
	r.Invalidate()

	err := r.Client.PostJSON(context.Background(), "/api/account/unlock", map[string]string{
		"password": pw,
	}, nil)
	var trustErr error
	snapCtx := r.Store.Snapshot().Context
	if err == nil && isLocalDesktopGateway(snapCtx) {
		action := "disable"
		if remember {
			action = "enable"
		}
		trustErr = r.applyTrustedLogin(action, pw, snapCtx.ConnectionID)
	}
	if err == nil {
		r.Store.RefreshAll(context.Background())
	}
	var toastMsg string
	st.mu.Lock()
	st.busy = false
	if err != nil {
		st.errMsg = err.Error()
	} else {
		st.password = ""
		st.rememberLoginInit = false
		st.trustedResumeTriedConn = ""
		if trustErr != nil {
			if remember {
				toastMsg = "已登录，记住我未能开启：" + trustErr.Error() + "。可在「设置 → 账户」中重试。"
			} else {
				toastMsg = "已登录，记住我未能关闭：" + trustErr.Error() + "。可在「设置 → 账户」中重试。"
			}
			st.notice = toastMsg
		}
	}
	st.mu.Unlock()
	if toastMsg != "" {
		r.PushToast(toastMsg)
	}
	r.Invalidate()
}

func (r *RootView) unlockWithDevice(binding, kind, reason string) {
	st := &r.account
	st.mu.Lock()
	st.busy = true
	st.errMsg = ""
	st.mu.Unlock()
	r.Invalidate()

	_, err := r.Devices.Unlock(context.Background(), kind, binding, reason)
	if err == nil {
		r.Store.RefreshAll(context.Background())
	}
	st.mu.Lock()
	st.busy = false
	if err != nil {
		st.errMsg = err.Error()
	} else {
		st.rememberLoginInit = false
		st.trustedResumeTriedConn = ""
	}
	st.mu.Unlock()
	r.Invalidate()
}

func (r *RootView) submitRecoveryReset() {
	st := &r.account
	st.mu.Lock()
	rk := strings.TrimSpace(st.recoveryKeyInput)
	pw, confirm := st.password, st.confirmPassword
	st.busy = true
	st.errMsg = ""
	st.mu.Unlock()
	r.Invalidate()

	var resp core.AccountView
	err := r.Client.PostJSON(context.Background(), "/api/account/recovery-reset", map[string]string{
		"recovery_key":     rk,
		"password":         pw,
		"confirm_password": confirm,
	}, &resp)
	if err == nil {
		r.Store.RefreshAll(context.Background())
	}
	st.mu.Lock()
	st.busy = false
	if err != nil {
		st.errMsg = err.Error()
	} else {
		st.password = ""
		st.confirmPassword = ""
		st.recoveryKeyInput = ""
		st.showRecoveryMode = false
		st.pendingRecoveryKey = resp.RecoveryKey
		st.recoveryConfirmed = false
		st.rememberLoginInit = false
		st.trustedResumeTriedConn = ""
	}
	st.mu.Unlock()
	r.Invalidate()
}
