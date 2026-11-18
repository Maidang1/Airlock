package views

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/sylvinus/agent-vm/internal/mygoclient/core"
)

type proxyConnectionItem struct {
	ID       string `json:"id"`
	Host     string `json:"host"`
	Port     string `json:"port"`
	Network  string `json:"network"`
	Upload   int64  `json:"upload"`
	Download int64  `json:"download"`
}

type proxyState struct {
	mu                     sync.Mutex
	activeTab              int // 0=Nodes, 1=Chain, 2=Connections/Logs
	importName             string
	importURL              string
	importYAML             string
	intervalStr            string
	hostDL                 bool
	defaultHostDLInit      bool
	showYAML               bool
	showImportForm         bool
	showPortDetails        bool
	search                 string
	portStr                string
	portInit               bool
	portChecked            bool
	portChecking           bool
	lastCheckedBackendPort int
	lastCheckedRunning     bool
	portReport             *core.HostPortReport
	dismissedJobErr        string
	chainIDs               []string
	delays                 map[string]int
	connections            []proxyConnectionItem
	logLines               []string
	busy                   bool
	errMsg                 string
	notice                 string
}

func isInfoPseudoNode(name string) bool {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return false
	}
	for _, kw := range []string{"剩余流量", "距离下次重置", "套餐到期", "流量剩余", "到期时间", "官网", "网址", "订阅更新"} {
		if strings.Contains(trimmed, kw) {
			return true
		}
	}
	return false
}

func formatProxyBytes(n int64) string {
	v := float64(max(0, n))
	switch {
	case v < 1024:
		return fmt.Sprintf("%.0f B", v)
	case v < 1024*1024:
		return fmt.Sprintf("%.1f KiB", v/1024)
	case v < 1024*1024*1024:
		return fmt.Sprintf("%.1f MiB", v/(1024*1024))
	default:
		return fmt.Sprintf("%.2f GiB", v/(1024*1024*1024))
	}
}

func formatProxyUsage(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	vals := map[string]int64{}
	for _, field := range strings.Split(raw, ";") {
		kv := strings.SplitN(strings.TrimSpace(field), "=", 2)
		if len(kv) != 2 {
			continue
		}
		if n, err := strconv.ParseInt(strings.TrimSpace(kv[1]), 10, 64); err == nil {
			vals[strings.TrimSpace(kv[0])] = n
		}
	}
	var parts []string
	if total := vals["total"]; total > 0 {
		used := vals["upload"] + vals["download"]
		parts = append(parts, fmt.Sprintf("已用 %s / %s", formatProxyBytes(used), formatProxyBytes(total)))
	}
	if exp := vals["expire"]; exp > 0 {
		parts = append(parts, "到期 "+time.Unix(exp, 0).Local().Format("2006-01-02"))
	}
	return strings.Join(parts, " · ")
}

func selectedProxyNodeName(px core.ProxyStatusView) string {
	if px.Selected == "" {
		return ""
	}
	for _, src := range px.Sources {
		for _, n := range src.Nodes {
			if n.ID == px.Selected {
				return n.Name
			}
		}
	}
	return px.Selected
}

func resolveProxyRouteLabel(px core.ProxyStatusView) string {
	if len(px.Route) == 0 {
		return "尚未选择出口节点"
	}
	nameByID := map[string]string{}
	for _, src := range px.Sources {
		for _, n := range src.Nodes {
			if n.ID != "" && n.Name != "" {
				nameByID[n.ID] = n.Name
			}
		}
	}
	var names []string
	for _, id := range px.Route {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" {
			continue
		}
		if name, ok := nameByID[trimmed]; ok {
			names = append(names, name)
		} else {
			names = append(names, trimmed)
		}
	}
	if len(names) == 0 {
		return "尚未选择出口节点"
	}
	return strings.Join(names, " → ")
}

func (r *RootView) renderProxyView(c *ui.Context, pal Palette, snap core.Snapshot) {
	st := &r.proxy
	px := snap.Proxy

	st.mu.Lock()
	if len(st.chainIDs) == 0 && len(px.Route) > 0 {
		st.chainIDs = append([]string(nil), px.Route...)
	}
	activeTab := st.activeTab
	busy := st.busy || snap.Status.Job.Running
	conns := append([]proxyConnectionItem(nil), st.connections...)
	logs := append([]string(nil), st.logLines...)
	st.mu.Unlock()

	PageScroll(c, pal, 860, 20, func() {
		ui.Column(c).Gap(16).FillWidth().Children(func() {
			// Status Header Card (macOS System Settings Inset Grouped style)
			Card(c, pal, func() {
				dnsSubtitle := ""
				if px.BootstrapDNS != "" {
					dnsSubtitle = "安全 DNS：" + px.BootstrapDNS
				}
				ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Gap(12).Wrap().Children(func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						SectionHeading(c, pal, "内置代理与节点路由（Mihomo）", dnsSubtitle)
						if px.NetworkOpen {
							PillBadge(c, px.StateLabel, pal.Success, pal.SuccessSoft)
						} else if px.StateLabel != "" {
							PillBadge(c, px.StateLabel, pal.Warning, pal.WarningSoft)
						}
					})
					ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
						if ui.Button(c, "重启代理核心").Disabled(busy || px.Selected == "").Clicked() {
							go r.proxyAction("restart", map[string]any{"port": r.effectiveProxyPort(snap)})
						}
						if ui.Button(c, "停止代理（关闭外网）").Disabled(busy || !px.Running).Clicked() {
							go r.proxyAction("stop", map[string]any{})
						}
					})
				})

				portDesc := fmt.Sprintf("%d", px.Port)
				if eff := r.effectiveProxyPort(snap); eff != px.Port && !px.Active && !px.Running {
					portDesc = fmt.Sprintf("%d（待应用端口 %d）", px.Port, eff)
				}
				ui.Column(c).
					FillWidth().
					Background(pal.ElevatedBg).
					Border(1, pal.Border).
					Radius(8).
					Padding(4, 12).
					Gap(2).
					Children(func() {
						KeyValueRow(c, pal, "当前监听端口", portDesc)
						InsetDivider(c, pal)
						KeyValueRow(c, pal, "当前节点链路", resolveProxyRouteLabel(px))
						InsetDivider(c, pal)
						KeyValueRow(c, pal, "安全拦截", "客机 UDP 已阻断 · IPv6 已阻断")
					})

				r.renderPortConfigBar(c, pal, snap, true)
			})

			ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
				ui.Segmented(c, &activeTab, "节点与订阅导入", "多跳代理链", "连接与审计日志").Label("代理功能视图")
			})
			st.mu.Lock()
			st.activeTab = activeTab
			st.mu.Unlock()

			switch activeTab {
			case 0:
				r.renderProxySourcesAndImport(c, pal, snap, false)
			case 1:
				r.renderProxyChainEditor(c, pal, px, busy)
			case 2:
				r.renderProxyTelemetry(c, pal, conns, logs)
			}
		})
	})
}

// renderProxySourcesAndImport renders the proxy subscription importer, source status/errors,
// and filterable node selector. Shared cleanly between Settings/Proxy view and Onboarding.
func (r *RootView) renderProxySourcesAndImport(c *ui.Context, pal Palette, snap core.Snapshot, onboarding bool) {
	st := &r.proxy
	px := snap.Proxy

	st.mu.Lock()
	if !st.defaultHostDLInit {
		st.defaultHostDLInit = true
		if !px.Active {
			st.hostDL = true
		}
		if strings.TrimSpace(st.intervalStr) == "" {
			st.intervalStr = "24"
		}
	}
	busy := st.busy || snap.Status.Job.Running
	errMsg := st.errMsg
	notice := st.notice
	hostDL := st.hostDL
	showYAML := st.showYAML
	showImport := st.showImportForm
	dismissedJobErr := st.dismissedJobErr
	searchQuery := strings.ToLower(strings.TrimSpace(st.search))
	delays := make(map[string]int, len(st.delays))
	for k, v := range st.delays {
		delays[k] = v
	}
	st.mu.Unlock()

	jobErr := ""
	if !snap.Status.Job.Running && (snap.Status.Job.Kind == "proxy" || snap.Status.Job.Kind == "proxy-update") &&
		snap.Status.Job.Error != "" && snap.Status.Job.Error != dismissedJobErr {
		jobErr = snap.Status.Job.Error
	}

	hasBanners := !px.CoreAvailable ||
		px.RouteError != "" ||
		(snap.LastError != "" && len(px.Sources) == 0) ||
		errMsg != "" ||
		(jobErr != "" && jobErr != errMsg) ||
		strings.TrimSpace(notice) != "" ||
		(snap.Status.Job.Running && (snap.Status.Job.Kind == "proxy" || snap.Status.Job.Kind == "proxy-update"))
	needImportForm := len(px.Sources) == 0 || showImport

	renderBanners := func() {
		if !px.CoreAvailable {
			ErrorBanner(c, pal, "未检测到内置代理核心二进制（mihomo），请先准备构建资源。")
		}
		if px.RouteError != "" {
			ErrorBanner(c, pal, "路由规则异常："+px.RouteError)
		}
		if snap.LastError != "" && len(px.Sources) == 0 {
			ErrorBanner(c, pal, snap.LastError)
		}
		ErrorBanner(c, pal, errMsg)
		if jobErr != "" && jobErr != errMsg {
			ErrorBanner(c, pal, jobErr)
		}
		InfoBanner(c, pal, notice)
		if snap.Status.Job.Running && (snap.Status.Job.Kind == "proxy" || snap.Status.Job.Kind == "proxy-update") {
			ui.Row(c).Gap(8).AlignItems(ui.Center).Children(func() {
				ui.Spinner(c)
				msg := snap.Status.Job.Message
				if msg == "" {
					msg = "正在处理代理订阅与节点验证…"
				}
				ui.Text(c, msg).FontSize(12).Bold().TextColor(pal.Text)
			})
		}
	}

	renderImportForm := func() {
		if !needImportForm {
			return
		}
		ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "导入订阅链接或 Mihomo / Clash YAML 配置").FontSize(13).Bold().TextColor(pal.Text)
			if len(px.Sources) > 0 {
				if ui.Button(c, "收起导入表单").Clicked() {
					st.mu.Lock()
					st.showImportForm = false
					st.mu.Unlock()
				}
			}
		})
		ui.Row(c).Gap(8).FillWidth().AlignItems(ui.End).Children(func() {
			ui.Column(c).Gap(3).Width(180).Children(func() {
				ui.Text(c, "订阅名称").FontSize(11).TextColor(pal.Muted)
				ui.TextInput(c, &st.importName).Label("订阅名称").Placeholder("默认订阅").FillWidth()
			})
			ui.Column(c).Gap(3).Grow(1).Children(func() {
				ui.Text(c, "Clash / Mihomo 订阅链接").FontSize(11).TextColor(pal.Muted)
				ui.TextInput(c, &st.importURL).Label("订阅链接").Placeholder("https://...（留空则下方粘贴 YAML）").FillWidth()
			})
			ui.Column(c).Gap(3).Width(92).Children(func() {
				ui.Text(c, "刷新间隔(h)").FontSize(11).TextColor(pal.Muted)
				ui.TextInput(c, &st.intervalStr).Label("更新间隔").Placeholder("24").FillWidth()
			})
		})
		if !onboarding || showYAML {
			ui.TextArea(c, &st.importYAML).Placeholder("或直接粘贴 Clash/Mihomo proxies YAML…").Height(84).FillWidth()
		}
		ui.Row(c).Gap(10).Wrap().AlignItems(ui.Center).Children(func() {
			if ui.PrimaryButton(c, "验证并导入配置").Disabled(busy).Clicked() {
				st.mu.Lock()
				name := strings.TrimSpace(st.importName)
				rawURL := strings.TrimSpace(st.importURL)
				yamlContent := st.importYAML
				interval, _ := strconv.Atoi(strings.TrimSpace(st.intervalStr))
				useHostDL := st.hostDL
				st.mu.Unlock()
				if name == "" {
					name = "默认订阅"
				}
				if interval <= 0 && rawURL != "" {
					interval = 24
				}
				go r.proxyAction("import", map[string]any{
					"name":           name,
					"url":            rawURL,
					"content":        yamlContent,
					"interval_hours": interval,
					"host_download":  useHostDL,
				})
			}
			ui.Checkbox(c, &st.hostDL, "允许通过宿主机网络下载订阅（首次配置建议开启）")
			if onboarding {
				yamlLabel := "手动粘贴 YAML"
				if showYAML {
					yamlLabel = "收起 YAML"
				}
				if ui.Button(c, yamlLabel).Clicked() {
					st.mu.Lock()
					st.showYAML = !st.showYAML
					st.mu.Unlock()
				}
			}
		})
	}

	hasAnyNodes := false
	for _, s := range px.Sources {
		if len(s.Nodes) > 0 {
			hasAnyNodes = true
			break
		}
	}

	renderSourcesList := func() {
		if len(px.Sources) == 0 {
			return
		}
		ui.Row(c).Gap(8).FillWidth().AlignItems(ui.Center).Children(func() {
			if hasAnyNodes {
				ui.SearchField(c, &st.search).Label("搜索节点").Placeholder("搜索节点名称或协议类型（例如 香港 / 日本 / anytls）…").Grow(1)
			} else {
				ui.Text(c, fmt.Sprintf("已配置 %d 个订阅来源", len(px.Sources))).FontSize(12).TextColor(pal.Muted).Grow(1)
			}
			if !showImport {
				if ui.Button(c, "+ 添加订阅 / 导入 YAML").Clicked() {
					st.mu.Lock()
					st.showImportForm = true
					st.mu.Unlock()
				}
			}
		})
		for _, src := range px.Sources {
			source := src
			var infoNodes []core.ProxyNode
			var realNodes []core.ProxyNode
			for _, node := range source.Nodes {
				if isInfoPseudoNode(node.Name) {
					infoNodes = append(infoNodes, node)
				} else {
					realNodes = append(realNodes, node)
				}
			}
			candidates := realNodes
			showHeaderInfoPills := len(realNodes) > 0 && len(infoNodes) > 0
			if len(candidates) == 0 {
				candidates = source.Nodes
			}
			var filtered []core.ProxyNode
			for _, node := range candidates {
				if searchQuery == "" ||
					strings.Contains(strings.ToLower(node.Name), searchQuery) ||
					strings.Contains(strings.ToLower(node.Type), searchQuery) {
					filtered = append(filtered, node)
				}
			}

			renderSourceContent := func() {
				ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Gap(12).Children(func() {
					ui.Column(c).Gap(2).Grow(1).Children(func() {
						ui.Text(c, source.Name).FontSize(14).Bold().TextColor(pal.Text)
						sub := source.Host
						if !source.Remote {
							sub = "本地 YAML 配置"
						}
						nodeCount := len(realNodes)
						if nodeCount == 0 {
							nodeCount = len(source.Nodes)
						}
						meta := fmt.Sprintf("%s · %d 个节点（忽略不支持 %d 个）", sub, nodeCount, len(source.Skipped))
						if !source.UpdatedAt.IsZero() {
							meta += " · 更新于 " + source.UpdatedAt.Local().Format("01-02 15:04")
						}
						ui.Text(c, meta).FontSize(11).TextColor(pal.Muted)
						if usage := formatProxyUsage(source.Usage); usage != "" {
							ui.Text(c, usage).FontSize(11).TextColor(pal.Muted)
						}
					})
					ui.Row(c).Gap(6).Children(func() {
						if source.Remote {
							if ui.Button(c, "刷新订阅").Disabled(busy).Clicked() {
								go r.proxyAction("refresh", map[string]any{"id": source.ID, "host_download": hostDL || !px.Active})
							}
						}
						if ui.Button(c, "删除").Disabled(busy).Clicked() {
							go r.proxyAction("delete", map[string]any{"id": source.ID})
						}
					})
				})
				if showHeaderInfoPills {
					ui.Row(c).Gap(6).Wrap().AlignItems(ui.Center).Children(func() {
						for _, info := range infoNodes {
							PillBadge(c, info.Name, pal.Muted, pal.ElevatedBg)
						}
					})
				}
				ErrorBanner(c, pal, source.Error)
				if len(source.Skipped) > 0 {
					var samples []string
					for i, iss := range source.Skipped {
						if i >= 3 {
							samples = append(samples, fmt.Sprintf("另有 %d 项", len(source.Skipped)-3))
							break
						}
						label := iss.Name
						if label == "" {
							label = iss.Type
						}
						if label != "" {
							samples = append(samples, label+"（"+iss.Reason+"）")
						} else {
							samples = append(samples, iss.Reason)
						}
					}
					ui.Text(c, "已跳过不支持节点："+strings.Join(samples, "；")).FontSize(11).TextColor(pal.Warning)
				}
				if len(filtered) == 0 && len(candidates) > 0 {
					ui.Text(c, "没有匹配当前搜索关键词的节点").FontSize(12).TextColor(pal.Muted)
					return
				}

				listBg := pal.ElevatedBg
				badgeBg := pal.CardBg
				if onboarding {
					listBg = pal.CardBg
					badgeBg = pal.ElevatedBg
				}
				renderNodesColumn := func() {
					ui.Column(c).
						FillWidth().
						Background(listBg).
						Border(1, pal.Border).
						Radius(8).
						Gap(0).
						Children(func() {
							for idx, node := range filtered {
								if idx > 0 {
									InsetDivider(c, pal)
								}
								n := node
								isSelected := px.Selected == n.ID
								rowBg := ui.Transparent
								if isSelected {
									rowBg = pal.AccentSoft
								}
								ui.Row(c).
									FillWidth().
									Background(rowBg).
									Padding(9, 12).
									Justify(ui.SpaceBetween).
									AlignItems(ui.Center).
									Gap(10).
									Children(func() {
										ui.Row(c).Gap(8).AlignItems(ui.Center).Grow(1).Shrink(1).Children(func() {
											dotCol := pal.SubtleText
											if isSelected {
												dotCol = pal.Accent
											}
											StatusDot(c, dotCol, 7)
											ui.Text(c, n.Name).FontSize(13).Bold().TextColor(pal.Text).SingleLine().Shrink(1)
											if n.Type != "" {
												PillBadge(c, strings.ToUpper(n.Type), pal.Muted, badgeBg)
											}
										})
										ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
											if ms, ok := delays[n.ID]; ok {
												PillBadge(c, fmt.Sprintf("%d ms", ms), pal.Success, pal.SuccessSoft)
											}
											if isInfoPseudoNode(n.Name) {
												PillBadge(c, "信息提示项", pal.Muted, badgeBg)
											}
											if ui.Button(c, "测速").Clicked() {
												go r.testProxyDelay(n.ID)
											}
											if !onboarding {
												if ui.Button(c, "+ 链路").Clicked() {
													st.mu.Lock()
													st.chainIDs = append(st.chainIDs, n.ID)
													st.activeTab = 1
													st.mu.Unlock()
												}
											}
											if isSelected {
												PillBadge(c, "当前出口", pal.Accent, pal.AccentSoft)
											} else if isInfoPseudoNode(n.Name) {
												if ui.Button(c, "设为出口").Disabled(busy).Clicked() {
													go r.proxyAction("select", map[string]any{"node_id": n.ID, "port": r.effectiveProxyPort(snap)})
												}
											} else if ui.PrimaryButton(c, "设为出口").Disabled(busy).Clicked() {
												go r.proxyAction("select", map[string]any{"node_id": n.ID, "port": r.effectiveProxyPort(snap)})
											}
										})
									})
							}
						})
				}

				if len(filtered) > 10 {
					ui.Box(c).FillWidth().Height(440).Children(func() {
						ui.Scroll(c).Fill().Children(renderNodesColumn)
					})
				} else {
					renderNodesColumn()
				}
			}

			if onboarding {
				ui.Column(c).Gap(8).FillWidth().Background(pal.ElevatedBg).Border(1, pal.Border).Radius(8).Padding(12).Children(renderSourceContent)
			} else {
				Card(c, pal, renderSourceContent)
			}
		}
	}

	if onboarding {
		ui.Column(c).Gap(10).FillWidth().Children(func() {
			if hasBanners || needImportForm {
				renderBanners()
				renderImportForm()
			}
			renderSourcesList()
		})
		return
	}

	if hasBanners || needImportForm {
		Card(c, pal, func() {
			renderBanners()
			renderImportForm()
		})
	}
	renderSourcesList()
}

func (r *RootView) renderProxyChainEditor(c *ui.Context, pal Palette, px core.ProxyStatusView, busy bool) {
	st := &r.proxy
	st.mu.Lock()
	chain := append([]string(nil), st.chainIDs...)
	st.mu.Unlock()

	Card(c, pal, func() {
		SectionHeading(c, pal, "多跳链式代理配置", "流量将从第 1 跳依次转发到最终出口节点")
		if len(chain) == 0 {
			ui.Text(c, "尚未添加链路节点，请先在节点列表中点击「+ 链路」。").FontSize(12).TextColor(pal.Muted)
		} else {
			ui.Column(c).FillWidth().Background(pal.ElevatedBg).Border(1, pal.Border).Radius(8).Gap(0).Children(func() {
				for idx, id := range chain {
					if idx > 0 {
						InsetDivider(c, pal)
					}
					i := idx
					nodeID := id
					nodeName := nodeID
					for _, src := range px.Sources {
						for _, n := range src.Nodes {
							if n.ID == nodeID {
								nodeName = n.Name + " (" + n.Type + ")"
							}
						}
					}
					ui.Row(c).FillWidth().Padding(9, 12).Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
						ui.Text(c, fmt.Sprintf("第 %d 跳：%s", i+1, nodeName)).FontSize(13).Bold().TextColor(pal.Text)
						if ui.Button(c, "移除").Clicked() {
							st.mu.Lock()
							if i < len(st.chainIDs) {
								st.chainIDs = append(st.chainIDs[:i], st.chainIDs[i+1:]...)
							}
							st.mu.Unlock()
						}
					})
				}
			})
		}
		ui.Row(c).Gap(8).Children(func() {
			if ui.PrimaryButton(c, "应用多跳代理链").Disabled(busy || len(chain) == 0).Clicked() {
				go r.proxyAction("route", map[string]any{"node_ids": chain, "port": r.effectiveProxyPort(r.Store.Snapshot())})
			}
			if ui.Button(c, "清空重选").Clicked() {
				st.mu.Lock()
				st.chainIDs = nil
				st.mu.Unlock()
			}
		})
	})
}

func (r *RootView) renderProxyTelemetry(c *ui.Context, pal Palette, conns []proxyConnectionItem, logs []string) {
	Card(c, pal, func() {
		ui.Row(c).FillWidth().Justify(ui.SpaceBetween).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "实时连接与代理策略审计").FontSize(14).Bold().TextColor(pal.Text)
			if ui.Button(c, "刷新连接与日志").Clicked() {
				go r.refreshProxyTelemetry()
			}
		})
		for _, line := range logs {
			ui.Text(c, "• "+line).FontSize(12).TextColor(pal.Muted)
		}
		InsetDivider(c, pal)
		if len(conns) == 0 {
			ui.Text(c, "当前无活跃代理连接或代理未运行（点击上方按钮刷新）").FontSize(12).TextColor(pal.Muted)
		} else {
			ui.Column(c).FillWidth().Background(pal.ElevatedBg).Border(1, pal.Border).Radius(8).Gap(0).Children(func() {
				for idx, conn := range conns {
					if idx > 0 {
						InsetDivider(c, pal)
					}
					item := conn
					ui.Row(c).FillWidth().Justify(ui.SpaceBetween).Padding(8, 12).Children(func() {
						ui.Text(c, item.Network+" "+item.Host+":"+item.Port).FontSize(12).TextColor(pal.Text)
						ui.Text(c, fmt.Sprintf("↑ %d B  ↓ %d B", item.Upload, item.Download)).FontSize(11).TextColor(pal.Muted)
					})
				}
			})
		}
	})
}

func (r *RootView) proxyAction(action string, payload map[string]any) {
	st := &r.proxy
	st.mu.Lock()
	st.busy = true
	st.errMsg = ""
	st.notice = ""
	st.dismissedJobErr = ""
	st.mu.Unlock()
	r.Invalidate()

	job, err := r.Store.RunTrackedJob(context.Background(), "/api/proxy/"+action, payload)
	snap := r.Store.Snapshot()
	px := snap.Proxy

	if err == nil && (action == "select" || action == "route" || action == "restart") {
		rep := core.InspectHostPorts(r.effectiveProxyPort(snap), snap, r.Client.Origin())
		st.mu.Lock()
		st.portReport = &rep
		st.portChecked = true
		st.lastCheckedBackendPort = px.Port
		st.lastCheckedRunning = px.Running
		st.mu.Unlock()
	}

	st.mu.Lock()
	st.busy = false
	if err != nil {
		st.errMsg = err.Error()
	} else {
		switch action {
		case "import":
			st.importYAML = ""
			st.importURL = ""
			st.showImportForm = false
			if len(px.Sources) > 0 {
				src := px.Sources[len(px.Sources)-1]
				st.notice = fmt.Sprintf("已导入订阅「%s」：%d 个节点（跳过 %d 个），请在下方选择真实出口节点并点击「设为出口」", src.Name, len(src.Nodes), len(src.Skipped))
			} else {
				st.notice = "代理订阅已导入"
			}
		case "refresh":
			st.notice = "订阅节点已刷新"
		case "select", "route":
			stateLabel := px.StateLabel
			if stateLabel == "" {
				stateLabel = "已应用"
			}
			st.notice = fmt.Sprintf("代理出口节点已应用（%s · 端口 %d）", stateLabel, px.Port)
		case "delete":
			st.notice = "已删除订阅来源"
		default:
			if job.Message != "" {
				st.notice = job.Message
			} else {
				st.notice = "操作已完成"
			}
		}
	}
	st.mu.Unlock()
	r.Invalidate()
}

func (r *RootView) testProxyDelay(nodeID string) {
	var resp struct {
		Delay int `json:"delay"`
	}
	err := r.Client.PostJSON(context.Background(), "/api/proxy/delay", map[string]string{"node_id": nodeID}, &resp)
	if err != nil {
		r.PushToast(err.Error())
		return
	}
	r.proxy.mu.Lock()
	if r.proxy.delays == nil {
		r.proxy.delays = map[string]int{}
	}
	r.proxy.delays[nodeID] = resp.Delay
	r.proxy.mu.Unlock()
	r.Invalidate()
}

func (r *RootView) refreshProxyTelemetry() {
	var connsResp struct {
		Connections []proxyConnectionItem `json:"connections"`
	}
	var logsResp struct {
		Lines []string `json:"lines"`
	}
	_ = r.Client.GetJSON(context.Background(), "/api/proxy/connections", &connsResp)
	_ = r.Client.GetJSON(context.Background(), "/api/proxy/logs", &logsResp)
	r.proxy.mu.Lock()
	r.proxy.connections = connsResp.Connections
	r.proxy.logLines = logsResp.Lines
	r.proxy.mu.Unlock()
	r.Invalidate()
}
