package microvm

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/sylvinus/agent-vm/internal/cage"
	"github.com/sylvinus/agent-vm/internal/exitpolicy"
)

// ExitProbeFunc resolves the current external exit IP and geolocation through cfg's proxy.
type ExitProbeFunc func(ctx context.Context, cfg cage.Config) (exitpolicy.Location, error)

// DefaultExitProbe queries the exit IP and geographic location strictly through cfg's proxy.
func DefaultExitProbe(ctx context.Context, cfg cage.Config) (exitpolicy.Location, error) {
	ip, err := cage.ProbeExitIP(ctx, cfg)
	if err != nil {
		return exitpolicy.Location{}, err
	}
	eg, err := cage.NewEgress(cfg)
	if err != nil {
		return exitpolicy.Location{}, err
	}
	tr := &http.Transport{Proxy: nil, DialContext: eg.DialContext, ForceAttemptHTTP2: true}
	defer tr.CloseIdleConnections()
	client := &http.Client{
		Transport:     tr,
		Timeout:       8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect refused") },
	}
	return exitpolicy.Lookup(ctx, client, net.ParseIP(ip))
}

var mandatoryCheckTitles = [][2]string{
	{"egress_policy", "环境外出站约束"},
	{"proxy_transport", "终端检测通道"},
	{"dns_transport", "DNS 与解析约束"},
	{"guest_settings", "系统时区与语言"},
	{"browser_settings", "浏览器时区与语言"},
	{"filesystem_boundary", "共享文件与宿主私有目录"},
	{"management_boundary", "宿主管理接口"},
	{"fail_closed", "代理故障关闭"},
	{"udp_policy", "UDP 出站约束"},
	{"ipv6_policy", "IPv6 出站约束"},
	{"exit_consistency", "出口符合已保存规则"},
}

// RunSafetyCheck verifies all 11 mandatory isolation invariants against the running
// MicroVM (including SetFault 503 fault injection and ExitPolicy checks) and grants GateOpen.
func RunSafetyCheck(ctx context.Context, cfg cage.Config, gate *cage.EgressGate, client *GuestClient, exitProbe ExitProbeFunc, hostCanaryPath string) (cage.SafetyReport, error) {
	if gate == nil || client == nil || exitProbe == nil {
		return cage.SafetyReport{}, errors.New("safety check requires gate, guest client, and exit probe")
	}
	if err := gate.BeginCheck(); err != nil {
		return cage.SafetyReport{}, err
	}
	granted := false
	defer func() {
		if !granted {
			gate.Close("安全检查未通过，外网保持关闭")
		}
	}()

	before := gate.Status()
	report := cage.SafetyReport{
		Policy:        cage.SafetyPolicyVersion,
		ID:            randomHex(16),
		RunID:         before.RunID,
		CheckID:       before.CheckID,
		Configuration: cfg.Fingerprint(),
		CreatedAt:     time.Now(),
		Checks:        make([]cage.SafetyCheck, 0, len(mandatoryCheckTitles)),
	}
	for _, item := range mandatoryCheckTitles {
		report.Checks = append(report.Checks, cage.SafetyCheck{
			ID:        item[0],
			Title:     item[1],
			Critical:  true,
			State:     cage.CheckUnknown,
			Evidence:  "尚未取得证据",
			CheckedAt: report.CreatedAt,
		})
	}
	setCheck := func(id string, state cage.CheckState, evidence, remedy string) {
		for i := range report.Checks {
			if report.Checks[i].ID == id {
				report.Checks[i].State = state
				report.Checks[i].Evidence = evidence
				report.Checks[i].Remedy = remedy
				report.Checks[i].CheckedAt = time.Now()
			}
		}
	}

	proxyHost, proxyPort := parseProxyHostPort(cfg.ProxyURL)
	nonce := randomHex(8)
	boundary, err := client.Probe(ctx, ProbeRequest{
		Stage:       "boundary",
		Timezone:    cfg.Timezone,
		GatewayIP:   DefaultGatewayIP,
		ProxyHost:   proxyHost,
		ProxyPort:   proxyPort,
		Nonce:       nonce,
		PrivateFile: hostCanaryPath,
	})
	if err != nil {
		return report, err
	}
	afterBoundary := gate.Status()

	if !boundary.PublicConnected && !boundary.LinkLocalConnected && afterBoundary.Counters.TCPDenied > before.Counters.TCPDenied {
		setCheck("egress_policy", cage.CheckPassed, "检查阶段公网与链路本地直连均被 EgressGate 拒绝", "")
	} else {
		setCheck("egress_policy", cage.CheckFailed, "检测到未经授权的直连或缺少拦截计数", "确认网关网络与 EgressGate 配置")
	}

	if boundary.UDPAttempted && afterBoundary.Counters.UDPDenied > before.Counters.UDPDenied {
		setCheck("udp_policy", cage.CheckPassed, "UDP 出站包已被宿主机用户态网络栈硬拦截", "")
	} else {
		setCheck("udp_policy", cage.CheckFailed, "未能证实 UDP 出站包被截断", "确认 EgressGate 拒绝所有 UDP 流量")
	}

	if !boundary.IPv6Connected && boundary.IPv6DefaultRoutes == 0 && boundary.IPv6GlobalAddresses == 0 {
		setCheck("ipv6_policy", cage.CheckPassed, "客机内核禁用 IPv6（0 条默认路由、0 个全局地址），IPv6 探测不可达", "")
	} else {
		setCheck("ipv6_policy", cage.CheckFailed, fmt.Sprintf("检测到 IPv6 通路或地址（routes=%d, addrs=%d）", boundary.IPv6DefaultRoutes, boundary.IPv6GlobalAddresses), "确认内核参数 ipv6.disable=1 且网络栈仅启用 IPv4")
	}

	if boundary.LocaleValid && boundary.TZ == cfg.Timezone && boundary.Lang == cfg.Locale && boundary.LCAll == cfg.Locale &&
		boundary.UTCOffsetExpected != nil && boundary.UTCOffsetSystem != nil && *boundary.UTCOffsetExpected == *boundary.UTCOffsetSystem {
		setCheck("guest_settings", cage.CheckPassed, "客机真实 TZif 偏移与 Locale 均与代理出口配置一致", "")
	} else {
		setCheck("guest_settings", cage.CheckFailed, fmt.Sprintf("客机时区或语言不匹配（tz=%q, lang=%q, valid=%t）", boundary.TZ, boundary.Lang, boundary.LocaleValid), "重新同步时区与 Locale")
	}

	if cfg.BrowserLanguage != "" && boundary.LocaleValid && boundary.Lang == cfg.Locale && boundary.TZ == cfg.Timezone {
		modeNote := "按需浏览器未启动，已锁定对齐后的时区与语言参数"
		if boundary.BrowserRunning {
			modeNote = "隔离浏览器运行中，时区与语言已与出口配置对齐"
		}
		setCheck("browser_settings", cage.CheckPassed, modeNote, "")
	} else {
		setCheck("browser_settings", cage.CheckFailed, "浏览器目标语言或时区未正确配置", "检查浏览器语言与时区配置")
	}

	if hostCanaryPath == "" {
		setCheck("filesystem_boundary", cage.CheckFailed, "缺少宿主机私有文件探测路径", "提供宿主机私有探针文件以证实文件隔离")
	} else if boundary.PrivateFile == "unreachable" {
		setCheck("filesystem_boundary", cage.CheckPassed, "客机无法读取宿主机私有文件，仅显式挂载目录可见", "")
	} else {
		setCheck("filesystem_boundary", cage.CheckFailed, fmt.Sprintf("宿主机私有路径状态异常：%s", boundary.PrivateFile), "确认未共享宿主机主目录或私有路径")
	}

	if !boundary.LinkLocalConnected && !boundary.ProxyEndpointConnected {
		setCheck("management_boundary", cage.CheckPassed, "客机无法触及宿主机代理监听端口与链路本地管理接口", "")
	} else {
		setCheck("management_boundary", cage.CheckFailed, "客机可直接访问宿主机代理端口或链路本地地址", "确认 Netstack NAT 为空且拦截回环与链路本地段")
	}

	networkReq := ProbeRequest{Stage: "network"}
	netObs, err := client.Probe(ctx, networkReq)
	if err != nil {
		return report, err
	}
	afterNetwork := gate.Status()

	if netObs.Egress.Status == http.StatusOK && netObs.Egress.IP != "" && afterNetwork.Counters.ProbeTCP > afterBoundary.Counters.ProbeTCP {
		setCheck("proxy_transport", cage.CheckPassed, fmt.Sprintf("经指定代理测得出口 IP %s", netObs.Egress.IP), "")
	} else {
		setCheck("proxy_transport", cage.CheckFailed, fmt.Sprintf("诊断通道异常（status=%d, ip=%q）", netObs.Egress.Status, netObs.Egress.IP), "检查托管代理连通性")
	}

	dnsBoundaryOK := !boundary.PublicDNSAnswered && !boundary.DNSTCPConnected && !boundary.DoTConnected &&
		boundary.Resolvers.Confined && boundary.Resolvers.Count > 0 && afterBoundary.Counters.DNSDenied > before.Counters.DNSDenied
	if dnsBoundaryOK && netObs.DNS.Status == http.StatusOK && afterNetwork.Counters.ProbeDNS > afterBoundary.Counters.ProbeDNS {
		setCheck("dns_transport", cage.CheckPassed, "直连公共 DNS 均被拒绝；解析器锁定网关且 DoH 经代理完成", "")
	} else {
		setCheck("dns_transport", cage.CheckFailed, "DNS 封闭或代理 DoH 探针未通过", "检查 /etc/resolv.conf 与 DoH 上游配置")
	}

	if err := gate.SetFault(true); err != nil {
		return report, err
	}
	faultBefore := gate.Status()
	faultObs, faultErr := client.Probe(ctx, networkReq)
	faultAfter := gate.Status()
	if err := gate.SetFault(false); err != nil {
		return report, err
	}
	if faultErr == nil && faultObs.Egress.Status == http.StatusServiceUnavailable && faultObs.DNS.Status == http.StatusServiceUnavailable &&
		faultAfter.Counters.FaultTCP > faultBefore.Counters.FaultTCP && faultAfter.Counters.FaultDNS > faultBefore.Counters.FaultDNS {
		setCheck("fail_closed", cage.CheckPassed, "代理故障注入下出站与 DNS 均返回 503，绝不回退直连", "")
	} else {
		setCheck("fail_closed", cage.CheckFailed, "代理故障演练未返回预期 503 状态", "检查 EgressGate Fail-Closed 熔断路径")
	}

	loc, locErr := exitProbe(ctx, cfg)
	if locErr != nil {
		setCheck("exit_consistency", cage.CheckUnknown, locErr.Error(), "重试或切换可用的非中国大陆代理节点")
	} else if netObs.Egress.IP != "" && loc.IP != netObs.Egress.IP {
		setCheck("exit_consistency", cage.CheckFailed, fmt.Sprintf("宿主探针出口 %s 与客机实测出口 %s 不一致", loc.IP, netObs.Egress.IP), "出口发生漂移，外网保持关闭")
	} else {
		state, msg := cfg.ExitPolicy.Evaluate(loc.IP, &loc)
		remedy := ""
		if state != cage.CheckPassed {
			remedy = "切换符合地理围栏与非中国大陆要求的代理节点"
		}
		setCheck("exit_consistency", state, msg, remedy)
	}

	if err := gate.Grant(report); err != nil {
		return report, err
	}
	granted = true
	return report, nil
}

// MonitorExit periodically re-checks exit IP geolocation and policy compliance,
// renewing the 90s EgressGate lease on success or closing it immediately on drift.
func MonitorExit(ctx context.Context, cfg cage.Config, gate *cage.EgressGate, reportID string, exitProbe ExitProbeFunc, interval time.Duration) {
	if gate == nil || exitProbe == nil || reportID == "" {
		return
	}
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			loc, err := exitProbe(probeCtx, cfg)
			cancel()
			if err != nil {
				gate.Close("出口巡检失败，外网已关闭：" + err.Error())
				return
			}
			state, msg := cfg.ExitPolicy.Evaluate(loc.IP, &loc)
			if state != cage.CheckPassed {
				gate.Close("出口策略违例，外网已熔断：" + msg)
				return
			}
			if err := gate.Renew(reportID); err != nil {
				gate.Close("门控续租失败，外网已关闭：" + err.Error())
				return
			}
		}
	}
}

func parseProxyHostPort(raw string) (string, int) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", 0
	}
	p, _ := strconv.Atoi(u.Port())
	return u.Hostname(), p
}

func randomHex(nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
