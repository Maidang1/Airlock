package views

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/sylvinus/agent-vm/internal/mygoclient/core"
)

type claudeAccountsState struct {
	mu          sync.Mutex
	editID      string
	editLabel   string
	editGroup   string
	busy        bool
	errMsg      string
	syncedSched bool
	mode        string
	policy      string
}

func (r *RootView) renderClaudeAccountsView(c *ui.Context, pal Palette, snap core.Snapshot) {
	st := &r.claudeAcct
	panel := snap.ClaudePanel

	st.mu.Lock()
	if !st.syncedSched && panel.Scheduling.Mode != "" {
		st.mode = panel.Scheduling.Mode
		st.policy = panel.Scheduling.SwitchPolicy
		st.syncedSched = true
	}
	if st.mode == "" {
		st.mode = "balanced"
	}
	if st.policy == "" {
		st.policy = "ask"
	}
	busy := st.busy
	errMsg := st.errMsg
	editID := st.editID
	st.mu.Unlock()

	PageScroll(c, pal, 980, 16, func() {
		ui.Column(c).Gap(14).FillWidth().Children(func() {
				// Scheduling policy card
				Card(c, pal, func() {
					ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
						SectionHeading(c, pal, "Claude 多账号管理与额度调度", "实时监控 5 小时与 7 天额度，按策略自动/询问切换账号")
						ui.Row(c).Gap(8).Children(func() {
							if ui.Button(c, "刷新全部额度").Disabled(busy).Clicked() {
								go r.claudeAcctMutate(map[string]any{"action": "usage_panel"})
							}
							if ui.PrimaryButton(c, "+ 新增账号登录").Disabled(!snap.Status.Ready || busy).Clicked() {
								r.CreateTerminal("claude-login")
							}
						})
					})
					ErrorBanner(c, pal, errMsg)
					ErrorBanner(c, pal, panel.DetectionError)
					ErrorBanner(c, pal, panel.UsageUnavailableReason)
					InfoBanner(c, pal, panel.LoginNotice)

					ui.Row(c).Gap(12).Wrap().AlignItems(ui.Center).Children(func() {
						ui.Column(c).Gap(4).Children(func() {
							ui.Text(c, "调度模式").FontSize(12).TextColor(pal.Muted)
							ui.Select(c, &st.mode, []string{"balanced", "sequential"})
						})
						ui.Column(c).Gap(4).Children(func() {
							ui.Text(c, "超限切换策略").FontSize(12).TextColor(pal.Muted)
							ui.Select(c, &st.policy, []string{"ask", "allow", "wait"})
						})
						if ui.Button(c, "保存调度策略").Disabled(busy).Clicked() {
							st.mu.Lock()
							mode, pol := st.mode, st.policy
							st.mu.Unlock()
							go r.claudeAcctMutate(map[string]any{
								"action": "scheduling",
								"scheduling": map[string]string{
									"mode":          mode,
									"switch_policy": pol,
								},
							})
						}
					})
				})

				// Detected default guest account slot
				if det := panel.Detected; det != nil {
					Card(c, pal, func() {
						ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
							ui.Column(c).Gap(2).Children(func() {
								ui.Text(c, "检测到默认 CLI 登录账号："+det.Email).FontSize(14).Bold().TextColor(pal.Text)
								meta := det.Plan
								if det.Organization != "" {
									meta += " · " + det.Organization
								}
								if meta != "" {
									ui.Text(c, meta).FontSize(12).TextColor(pal.Muted)
								}
							})
							ui.Row(c).Gap(8).Children(func() {
								if ui.Button(c, "刷新").Disabled(det.UsageChecking || busy).Clicked() {
									go r.claudeAcctMutate(map[string]any{"action": "refresh_default"})
								}
								if ui.PrimaryButton(c, "加入调度列表").Disabled(busy).Clicked() {
									id := det.Identity
									go r.claudeAcctMutate(map[string]any{"action": "join", "identity": id})
								}
							})
						})
						renderClaudeQuotaBars(c, pal, det.Quota)
					})
				}

				// Managed accounts list
				if len(panel.Accounts) == 0 {
					Card(c, pal, func() {
						ui.Text(c, "暂无已加入调度的 Claude 账号，可点击右上角「+ 新增账号登录」或将检测到的账号加入。").FontSize(13).TextColor(pal.Muted)
					})
					return
				}

				for idx, acct := range panel.Accounts {
					i := idx
					a := acct
					Card(c, pal, func() {
						ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
							ui.Column(c).Gap(2).Children(func() {
								title := a.Label
								if title == "" {
									title = a.Email
								}
								if title == "" {
									title = a.ID
								}
								ui.Text(c, fmt.Sprintf("#%d %s", i+1, title)).FontSize(14).Bold().TextColor(pal.Text)
								info := strings.TrimSpace(strings.Join([]string{a.Email, a.Plan, a.Organization}, " · "))
								if info != "" {
									ui.Text(c, info).FontSize(12).TextColor(pal.Muted)
								}
							})
							ui.Row(c).Gap(6).Wrap().AlignItems(ui.Center).Children(func() {
								if a.Enabled {
									PillBadge(c, "已启用", pal.Success, pal.SuccessSoft)
								} else {
									PillBadge(c, "已暂停", pal.Muted, pal.ElevatedBg)
								}
								if a.HandoffGroup != "" {
									PillBadge(c, "组: "+a.HandoffGroup, pal.Accent, pal.AccentSoft)
								}
								if i > 0 {
									if ui.Button(c, "↑").Disabled(busy).Clicked() {
										go r.reorderClaudeAccount(panel.Accounts, i, i-1, panel.Scheduling)
									}
								}
								if i < len(panel.Accounts)-1 {
									if ui.Button(c, "↓").Disabled(busy).Clicked() {
										go r.reorderClaudeAccount(panel.Accounts, i, i+1, panel.Scheduling)
									}
								}
								if ui.Button(c, "刷新").Disabled(a.UsageChecking || busy).Clicked() {
									go r.claudeAcctMutate(map[string]any{"action": "refresh", "id": a.ID})
								}
								toggleLbl := "暂停"
								if !a.Enabled {
									toggleLbl = "启用"
								}
								if ui.Button(c, toggleLbl).Disabled(busy).Clicked() {
									next := !a.Enabled
									go r.claudeAcctMutate(map[string]any{"action": "update", "id": a.ID, "enabled": &next})
								}
								if ui.Button(c, "编辑").Clicked() {
									st.mu.Lock()
									if st.editID == a.ID {
										st.editID = ""
									} else {
										st.editID = a.ID
										st.editLabel = a.Label
										st.editGroup = a.HandoffGroup
									}
									st.mu.Unlock()
								}
								if a.Email == "" {
									if ui.Button(c, "撤销").Disabled(busy).Clicked() {
										go r.claudeAcctMutate(map[string]any{"action": "cancel", "id": a.ID})
									}
								}
							})
						})

						renderClaudeQuotaBars(c, pal, a.Quota)

						if editID == a.ID {
							ui.Row(c).Gap(8).Wrap().AlignItems(ui.Center).Children(func() {
								ui.TextInput(c, &st.editLabel).Placeholder("账号备注名").Width(200)
								ui.TextInput(c, &st.editGroup).Placeholder("接力分组名").Width(160)
								if ui.PrimaryButton(c, "保存信息").Disabled(busy).Clicked() {
									st.mu.Lock()
									lbl, grp := strings.TrimSpace(st.editLabel), strings.TrimSpace(st.editGroup)
									st.editID = ""
									st.mu.Unlock()
									go r.claudeAcctMutate(map[string]any{
										"action":        "update",
										"id":            a.ID,
										"label":         lbl,
										"handoff_group": &grp,
									})
								}
							})
						}
					})
				}
			})
	})
}

func renderClaudeQuotaBars(c *ui.Context, pal Palette, q *core.ClaudeQuota) {
	if q == nil {
		return
	}
	ui.Column(c).Gap(6).FillWidth().Children(func() {
		if q.FiveHour != nil {
			ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, fmt.Sprintf("5 小时额度：%.0f%%", q.FiveHour.UsedPercentage)).FontSize(12).TextColor(pal.Text).Width(130)
				ui.Progress(c, q.FiveHour.UsedPercentage/100.0).Grow(1)
			})
		}
		if q.SevenDay != nil {
			ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, fmt.Sprintf("7 天额度：%.0f%%", q.SevenDay.UsedPercentage)).FontSize(12).TextColor(pal.Text).Width(130)
				ui.Progress(c, q.SevenDay.UsedPercentage/100.0).Grow(1)
			})
		}
	})
}

func (r *RootView) reorderClaudeAccount(list []core.ClaudeAccount, from, to int, sched core.ClaudeScheduling) {
	order := make([]string, len(list))
	for i, a := range list {
		order[i] = a.ID
	}
	order[from], order[to] = order[to], order[from]
	if sched.Mode == "" {
		sched.Mode = "balanced"
	}
	if sched.SwitchPolicy == "" {
		sched.SwitchPolicy = "ask"
	}
	r.claudeAcctMutate(map[string]any{
		"action":     "scheduling",
		"scheduling": sched,
		"order":      order,
	})
}

func (r *RootView) claudeAcctMutate(body map[string]any) {
	st := &r.claudeAcct
	st.mu.Lock()
	st.busy = true
	st.errMsg = ""
	st.mu.Unlock()
	r.Invalidate()

	err := r.Client.PostJSON(context.Background(), "/api/claude-accounts", body, nil)
	st.mu.Lock()
	st.busy = false
	if err != nil {
		st.errMsg = err.Error()
	}
	st.mu.Unlock()
	r.Store.TriggerRefresh()
	r.Invalidate()
}
