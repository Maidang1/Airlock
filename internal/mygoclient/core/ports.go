package core

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// PortState classifies the host binding status of a loopback TCP port.
type PortState string

const (
	PortStateAvailable   PortState = "available"
	PortStateSelfRunning PortState = "self_running"
	PortStateConflict    PortState = "conflict"
	PortStateInvalid     PortState = "invalid"
)

// PortCheckResult describes a probed loopback TCP port or Cage local service endpoint.
type PortCheckResult struct {
	Name        string    `json:"name"`
	Address     string    `json:"address"`
	Port        int       `json:"port"`
	Editable    bool      `json:"editable"`
	State       PortState `json:"state"`
	Detail      string    `json:"detail"`
	ProcessName string    `json:"process_name,omitempty"`
	PID         int       `json:"pid,omitempty"`
}

// HostPortReport summarizes Cage local ports and detected host listener conflicts.
type HostPortReport struct {
	CandidateProxy PortCheckResult   `json:"candidate_proxy"`
	CageServices   []PortCheckResult `json:"cage_services"`
	HostListeners  []PortCheckResult `json:"host_listeners"`
	SuggestedPort  int               `json:"suggested_port"`
	CheckedAt      time.Time         `json:"checked_at"`
}

var reservedCommonProxyPorts = map[int]bool{
	1080: true, 1081: true, 1086: true, 1087: true,
	7890: true, 7891: true, 7892: true, 7897: true, 7898: true, 7899: true,
	8080: true, 8118: true, 8888: true, 9090: true, 9091: true,
}

var hostScanCandidates = []struct {
	Port  int
	Label string
}{
	{17896, "Cage 默认独立代理端口"},
	{7890, "Clash / Mihomo 常见混合端口"},
	{7891, "Clash 常见 SOCKS 端口"},
	{7897, "Mihomo Party 常见端口"},
	{1080, "通用 SOCKS5 代理端口"},
	{1087, "Shadowrocket / V2RayU 常见端口"},
	{9090, "代理控制面板常见端口"},
}

// CheckLoopbackPort checks whether 127.0.0.1:<port> is available for Cage or already
// occupied on the host. When selfOwned is true, the port is treated as actively used by
// this Cage instance's managed proxy core rather than a conflict.
func CheckLoopbackPort(port int, selfOwned bool) PortCheckResult {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	if port < 1024 || port > 65535 {
		return PortCheckResult{
			Address: addr,
			Port:    port,
			State:   PortStateInvalid,
			Detail:  "端口须在 1024–65535 之间（避开系统特权端口）",
		}
	}
	if selfOwned {
		return PortCheckResult{
			Address: addr,
			Port:    port,
			State:   PortStateSelfRunning,
			Detail:  "当前 Cage 实例自带代理核心正在使用此端口",
		}
	}

	conflict := false
	// Dial first so wildcard (0.0.0.0 / ::) listeners are detected reliably on macOS.
	if conn, err := net.DialTimeout("tcp4", addr, 45*time.Millisecond); err == nil {
		_ = conn.Close()
		conflict = true
	} else {
		ln, listenErr := net.Listen("tcp4", addr)
		if listenErr != nil {
			conflict = true
		} else {
			_ = ln.Close()
		}
	}

	if !conflict {
		return PortCheckResult{
			Address: addr,
			Port:    port,
			State:   PortStateAvailable,
			Detail:  "端口空闲可用，未与宿主机冲突",
		}
	}

	proc, pid := lookupListeningProcess(port)
	detail := "已被宿主机其他程序占用，请更换端口"
	if proc != "" && pid > 0 {
		detail = fmt.Sprintf("已被宿主机进程 %s (PID %d) 占用，请更换端口", proc, pid)
	} else if proc != "" {
		detail = fmt.Sprintf("已被宿主机进程 %s 占用，请更换端口", proc)
	}
	return PortCheckResult{
		Address:     addr,
		Port:        port,
		State:       PortStateConflict,
		Detail:      detail,
		ProcessName: proc,
		PID:         pid,
	}
}

func lookupListeningProcess(port int) (string, int) {
	if runtime.GOOS != "darwin" || port < 1 || port > 65535 {
		return "", 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, "/usr/sbin/lsof", "-nP", fmt.Sprintf("-iTCP:%d", port), "-sTCP:LISTEN", "-Fpc").Output()
	if err != nil && len(out) == 0 {
		return "", 0
	}
	var pid int
	var proc string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "p") && pid == 0 {
			if parsed, convErr := strconv.Atoi(line[1:]); convErr == nil {
				pid = parsed
			}
		} else if strings.HasPrefix(line, "c") && proc == "" {
			proc = strings.ReplaceAll(line[1:], `\x20`, " ")
		}
		if pid > 0 && proc != "" {
			break
		}
	}
	return proc, pid
}

// SuggestFreeProxyPort scans 17897..17999 (skipping common host proxy ports) and returns
// the first unbound loopback port, falling back to an OS-assigned ephemeral port if needed.
func SuggestFreeProxyPort(startFrom int, selfPort int, selfRunning bool) (int, error) {
	begin := 17897
	if startFrom > begin && startFrom <= 17999 {
		begin = startFrom
	}
	tryPort := func(p int) bool {
		if reservedCommonProxyPorts[p] {
			return false
		}
		if selfRunning && p == selfPort {
			return false
		}
		return CheckLoopbackPort(p, false).State == PortStateAvailable
	}
	for p := begin; p <= 17999; p++ {
		if tryPort(p) {
			return p, nil
		}
	}
	for p := 17897; p < begin; p++ {
		if tryPort(p) {
			return p, nil
		}
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port, nil
}

// InspectHostPorts checks candidatePort, Cage's local services, common host proxy ports,
// and computes a conflict-free fallback port suggestion.
func InspectHostPorts(candidatePort int, snap Snapshot, controllerOrigin string) HostPortReport {
	if candidatePort == 0 {
		candidatePort = snap.Proxy.Port
	}
	if candidatePort == 0 {
		candidatePort = 17896
	}
	selfOwned := snap.Proxy.Running && snap.Proxy.Port == candidatePort
	cand := CheckLoopbackPort(candidatePort, selfOwned)
	cand.Name = "Cage 隔离代理核心 (Mihomo)"
	cand.Editable = true

	ctrlAddr := "127.0.0.1:动态端口"
	ctrlPort := 0
	if u, err := url.Parse(strings.TrimSpace(controllerOrigin)); err == nil && u.Host != "" {
		ctrlAddr = u.Host
		if p, convErr := strconv.Atoi(u.Port()); convErr == nil {
			ctrlPort = p
		}
	}
	ctrlEntry := PortCheckResult{
		Name:     "Cage 本地工作台控制服务",
		Address:  ctrlAddr,
		Port:     ctrlPort,
		Editable: false,
		State:    PortStateSelfRunning,
		Detail:   "系统动态分配空闲回环端口（自动防冲突）",
	}

	sshState := PortStateAvailable
	if snap.Status.VMRunning {
		sshState = PortStateSelfRunning
	}
	sshEntry := PortCheckResult{
		Name:     "客机 SSH 安全通道 (Lima)",
		Address:  "127.0.0.1:动态端口",
		Editable: false,
		State:    sshState,
		Detail:   "启动客机时由 Lima 自动分配空闲回环端口",
	}

	var listeners []PortCheckResult
	for _, item := range hostScanCandidates {
		if snap.Proxy.Running && snap.Proxy.Port == item.Port {
			continue
		}
		res := CheckLoopbackPort(item.Port, false)
		if res.State == PortStateConflict {
			res.Name = item.Label
			listeners = append(listeners, res)
		}
	}

	suggested, _ := SuggestFreeProxyPort(candidatePort+1, snap.Proxy.Port, snap.Proxy.Running)

	return HostPortReport{
		CandidateProxy: cand,
		CageServices:   []PortCheckResult{cand, ctrlEntry, sshEntry},
		HostListeners:  listeners,
		SuggestedPort:  suggested,
		CheckedAt:      time.Now(),
	}
}
