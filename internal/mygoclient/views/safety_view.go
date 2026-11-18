package views

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"github.com/egoist/mygo/ui"
	"github.com/sylvinus/agent-vm/internal/mygoclient/core"
)

type safetyViewState struct {
	mu     sync.Mutex
	busy   bool
	errMsg string
}

var defaultSafetyCheckCatalog = []core.SafetyCheck{
	{ID: "egress_policy", Title: "环境外出站约束", Critical: true, State: "unknown", Evidence: "尚未取得证据"},
	{ID: "proxy_transport", Title: "终端检测通道", Critical: true, State: "unknown", Evidence: "尚未取得证据"},
	{ID: "dns_transport", Title: "DNS 与解析约束", Critical: true, State: "unknown", Evidence: "尚未取得证据"},
	{ID: "guest_settings", Title: "系统时区与语言", Critical: true, State: "unknown", Evidence: "尚未取得证据"},
	{ID: "browser_settings", Title: "浏览器时区与语言", Critical: true, State: "unknown", Evidence: "尚未取得证据"},
	{ID: "filesystem_boundary", Title: "共享文件与宿主私有目录", Critical: true, State: "unknown", Evidence: "尚未取得证据"},
	{ID: "management_boundary", Title: "宿主管理接口", Critical: true, State: "unknown", Evidence: "尚未取得证据"},
	{ID: "fail_closed", Title: "代理故障关闭", Critical: true, State: "unknown", Evidence: "尚未取得证据"},
	{ID: "udp_policy", Title: "UDP 出站约束", Critical: true, State: "unknown", Evidence: "尚未取得证据"},
	{ID: "ipv6_policy", Title: "IPv6 出站约束", Critical: true, State: "unknown", Evidence: "尚未取得证据"},
	{ID: "exit_consistency", Title: "出口符合已保存规则", Critical: true, State: "unknown", Evidence: "尚未取得证据"},
	{ID: "third_party_reputation", Title: "第三方 IP 信誉", Critical: false, State: "unknown", Evidence: "尚未取得证据"},
	{ID: "language_suggestion", Title: "地区语言建议", Critical: false, State: "unknown", Evidence: "尚未取得证据"},
}

func resolveSafetyChecks(report *core.SafetyReport) []core.SafetyCheck {
	if report == nil || len(report.Checks) == 0 {
		out := make([]core.SafetyCheck, len(defaultSafetyCheckCatalog))
		copy(out, defaultSafetyCheckCatalog)
		return out
	}
	byID := make(map[string]core.SafetyCheck, len(report.Checks))
	for _, chk := range report.Checks {
		byID[chk.ID] = chk
	}
	out := make([]core.SafetyCheck, 0, len(defaultSafetyCheckCatalog)+len(report.Checks))
	seen := map[string]bool{}
	for _, def := range defaultSafetyCheckCatalog {
		if actual, ok := byID[def.ID]; ok {
			if actual.Title == "" {
				actual.Title = def.Title
			}
			out = append(out, actual)
		} else {
			out = append(out, def)
		}
		seen[def.ID] = true
	}
	for _, chk := range report.Checks {
		if !seen[chk.ID] {
			out = append(out, chk)
		}
	}
	return out
}

func (r *RootView) renderSafetyView(c *ui.Context, pal Palette, snap core.Snapshot) {
	st := &r.safety
	st.mu.Lock()
	busy := st.busy
	errMsg := st.errMsg
	st.mu.Unlock()

	safety := snap.Status.Safety
	gate := safety.Gate
	checks := resolveSafetyChecks(safety.Report)

	var passCnt, warnCnt, failCnt, unknownCnt int
	for _, chk := range checks {
		switch chk.State {
		case "pass":
			passCnt++
		case "warn":
			warnCnt++
		case "fail":
			failCnt++
		default:
			unknownCnt++
		}
	}

	PageScroll(c, pal, 900, 20, func() {
		ui.Column(c).Gap(14).FillWidth().Children(func() {
			Card(c, pal, func() {
				ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Gap(12).Wrap().Children(func() {
					SectionHeading(c, pal, "隔离网络与安全检查", "确保所有客机流量强制经由受管出口，杜绝直连泄露")
					if gate != nil && gate.Mode == "open" {
						PillBadge(c, "安全门控：已放行", pal.Success, pal.SuccessSoft)
					} else {
						PillBadge(c, "安全门控：已关闭", pal.Warning, pal.WarningSoft)
					}
				})
				ErrorBanner(c, pal, errMsg)
				ErrorBanner(c, pal, safety.Error)

				ui.Column(c).FillWidth().Background(pal.ElevatedBg).Border(1, pal.Border).Radius(8).Padding(4, 12).Gap(2).Children(func() {
					exitIP := safety.ExitIP
					if exitIP == "" {
						exitIP = "未验证 / 已阻断"
					}
					KeyValueRow(c, pal, "已验证出口 IP", exitIP)
					if loc := snap.Status.Location; loc != nil && loc.IP != "" {
						InsetDivider(c, pal)
						KeyValueRow(c, pal, "地理与语言环境", fmt.Sprintf("%s (%s) · %s · %s", loc.Country, loc.CountryCode, loc.Timezone, loc.Locale))
					}
					InsetDivider(c, pal)
					KeyValueRow(c, pal, "检测结果统计", fmt.Sprintf("通过 %d · 警告 %d · 失败 %d · 未检测 %d", passCnt, warnCnt, failCnt, unknownCnt))
					if gate != nil {
						InsetDivider(c, pal)
						KeyValueRow(c, pal, "阻断直连统计", fmt.Sprintf("公网 %d · 管理面 %d · UDP %d · IPv6 %d · 公网DNS %d",
							gate.Counters.PublicBlocked, gate.Counters.ManagementBlocked,
							gate.Counters.UDPBlocked, gate.Counters.IPv6Blocked, gate.Counters.PublicDNSBlocked))
					}
				})

				ui.Row(c).Gap(8).Wrap().Children(func() {
					if ui.PrimaryButton(c, "执行完整安全检查").Disabled(busy || !snap.Status.VMRunning).Clicked() {
						go r.safetyAction("/api/safety/check")
					}
					if ui.Button(c, "立即关闭外网").Disabled(busy || !snap.Status.VMRunning).Clicked() {
						go r.safetyAction("/api/safety/close")
					}
					if ui.Button(c, "导出安全审计报告").Clicked() {
						go r.exportSafetyReport()
					}
				})
			})

			// macOS static exit candidate confirmation when present
			if len(safety.MacOSExitCandidate) > 0 && string(safety.MacOSExitCandidate) != "null" {
				var cand struct {
					IP string `json:"ip"`
				}
				if json.Unmarshal(safety.MacOSExitCandidate, &cand) == nil && cand.IP != "" {
					Card(c, pal, func() {
						ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
							ui.Column(c).Gap(2).Children(func() {
								ui.Text(c, "检测到一致的固定出口候选 IP："+cand.IP).FontSize(13).Bold().TextColor(pal.Text)
								ui.Text(c, "确认后将锁定 macOS 客机的静态出口校验基线").FontSize(11).TextColor(pal.Muted)
							})
							if ui.PrimaryButton(c, "确认绑定出口策略").Clicked() {
								go r.postSimple("/api/macos/exit-policy", map[string]string{"ip": cand.IP})
							}
						})
					})
				}
			}

			// Always display every check item and its live status
			Card(c, pal, func() {
				ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
					ui.Text(c, fmt.Sprintf("全部安全检测项（共 %d 项）", len(checks))).FontSize(14).Bold().TextColor(pal.Text)
					ui.Text(c, "11 项关键必过 · 2 项参考项").FontSize(11).TextColor(pal.Muted)
				})
				ui.Column(c).FillWidth().Background(pal.ElevatedBg).Border(1, pal.Border).Radius(8).Gap(0).Children(func() {
					for idx, chk := range checks {
						if idx > 0 {
							InsetDivider(c, pal)
						}
						item := chk
						ui.Column(c).FillWidth().Padding(10, 12).Gap(4).Children(func() {
							ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Gap(8).Children(func() {
								ui.Row(c).Gap(6).AlignItems(ui.Center).Grow(1).Shrink(1).Children(func() {
									ui.Text(c, item.Title).FontSize(13).Bold().TextColor(pal.Text).Shrink(1)
									if item.Critical {
										PillBadge(c, "关键项", pal.Muted, pal.CardBg)
									} else {
										PillBadge(c, "参考项", pal.Muted, pal.CardBg)
									}
								})
								switch item.State {
								case "pass":
									PillBadge(c, "通过", pal.Success, pal.SuccessSoft)
								case "warn":
									PillBadge(c, "警告", pal.Warning, pal.WarningSoft)
								case "fail":
									PillBadge(c, "失败", pal.Danger, pal.DangerSoft)
								default:
									PillBadge(c, "未检测", pal.Muted, pal.CardBg)
								}
							})
							if item.Evidence != "" {
								ui.Text(c, item.Evidence).FontSize(11).TextColor(pal.Muted)
							}
							if item.Remedy != "" {
								ui.Text(c, "建议："+item.Remedy).FontSize(11).TextColor(pal.Warning)
							}
						})
					}
				})
			})
		})
	})
}

func (r *RootView) safetyAction(path string) {
	st := &r.safety
	st.mu.Lock()
	st.busy = true
	st.errMsg = ""
	st.mu.Unlock()
	r.Invalidate()

	err := r.Client.PostJSON(context.Background(), path, map[string]any{}, nil)
	st.mu.Lock()
	st.busy = false
	if err != nil {
		st.errMsg = err.Error()
	}
	st.mu.Unlock()
	r.Store.TriggerRefresh()
	r.Invalidate()
}

func (r *RootView) exportSafetyReport() {
	if r.Bridge == nil {
		return
	}
	dest, err := r.Bridge.SaveDialog(core.NativeDialogOptions{
		Title:       "保存安全审计报告",
		DefaultPath: "cage-safety-report.json",
		ButtonLabel: "保存",
	})
	if err != nil || dest == "" {
		return
	}
	raw, _, err := r.Client.DoRaw(context.Background(), "GET", "/api/safety/report", nil, "")
	if err != nil {
		r.PushToast("读取安全报告失败：" + err.Error())
		return
	}
	if err := os.WriteFile(dest, raw, 0600); err != nil {
		r.PushToast("写入文件失败：" + err.Error())
		return
	}
	r.PushToast("安全审计报告已保存")
}
