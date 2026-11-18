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

func (r *RootView) renderSafetyView(c *ui.Context, pal Palette, snap core.Snapshot) {
	st := &r.safety
	st.mu.Lock()
	busy := st.busy
	errMsg := st.errMsg
	st.mu.Unlock()

	safety := snap.Status.Safety
	gate := safety.Gate

	PageScroll(c, pal, 980, 16, func() {
		ui.Column(c).Gap(14).FillWidth().Children(func() {
				Card(c, pal, func() {
					ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
						SectionHeading(c, pal, "隔离网络与安全检查", "确保所有客机流量强制经由受管出口，杜绝直连泄露")
						if gate != nil && gate.Mode == "open" {
							PillBadge(c, "安全门控：已放行", pal.Success, pal.SuccessSoft)
						} else {
							PillBadge(c, "安全门控：已关闭", pal.Warning, pal.WarningSoft)
						}
					})
					ErrorBanner(c, pal, errMsg)
					ErrorBanner(c, pal, safety.Error)

					if safety.ExitIP != "" {
						KeyValueRow(c, pal, "已验证出口 IP", safety.ExitIP)
					}
					if loc := snap.Status.Location; loc != nil && loc.IP != "" {
						KeyValueRow(c, pal, "地理与语言环境", fmt.Sprintf("%s (%s) · %s · %s", loc.Country, loc.CountryCode, loc.Timezone, loc.Locale))
					}
					if gate != nil {
						KeyValueRow(c, pal, "阻断直连统计", fmt.Sprintf("公网 %d · 管理面 %d · UDP %d · IPv6 %d · 公网DNS %d",
							gate.Counters.PublicBlocked, gate.Counters.ManagementBlocked,
							gate.Counters.UDPBlocked, gate.Counters.IPv6Blocked, gate.Counters.PublicDNSBlocked))
					}

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

				if safety.Report != nil && len(safety.Report.Checks) > 0 {
					Card(c, pal, func() {
						ui.Text(c, "详细安全验证项（共 "+fmt.Sprintf("%d", len(safety.Report.Checks))+" 项）").FontSize(14).Bold().TextColor(pal.Text)
						ui.Column(c).Gap(8).FillWidth().Children(func() {
							for _, chk := range safety.Report.Checks {
								item := chk
								ui.Column(c).Background(pal.ElevatedBg).Border(1, pal.Border).Radius(6).Padding(10).Gap(4).Children(func() {
									ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
										ui.Text(c, item.Title).FontSize(13).Bold().TextColor(pal.Text)
										switch item.State {
										case "pass":
											PillBadge(c, "通过", pal.Success, pal.SuccessSoft)
										case "warn":
											PillBadge(c, "警告", pal.Warning, pal.WarningSoft)
										default:
											PillBadge(c, "失败", pal.Danger, pal.DangerSoft)
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
				}
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
