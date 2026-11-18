//go:build darwin

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/sylvinus/agent-vm/internal/macnative"
	"golang.org/x/sys/unix"
)

func main() {
	macnative.PinEnvironment()
	if err := runInstaller(); err != nil {
		sendJSON(map[string]string{"status": "error", "message": err.Error()})
		os.Exit(1)
	}
}

func runInstaller() error {
	if len(os.Args) != 2 {
		return errors.New("安装任务无效")
	}
	plan, err := readPlan(os.Args[1])
	if err != nil {
		return err
	}
	targetDir := filepath.Dir(plan.Target)
	requestAuth := unix.Access(targetDir, unix.W_OK) != nil

	_ = os.RemoveAll(plan.Backup)
	if out, err := exec.Command("/usr/bin/ditto", plan.Target, plan.Backup).CombinedOutput(); err != nil {
		return fmt.Errorf("无法备份原应用：%v %s", err, strings.TrimSpace(string(out)))
	}
	if _, err := macnative.SigningInfo(plan.Backup, true, plan.Team); err != nil {
		return err
	}
	sendJSON(map[string]string{"status": "ready"})

	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil || line != "COMMIT\n" {
		return nil
	}

	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		_, terminated, ok := macnative.RunningAppInfo(int(plan.ParentPID))
		if !ok || terminated {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, terminated, ok := macnative.RunningAppInfo(int(plan.ParentPID)); ok && !terminated {
		return errors.New("Cage 尚未退出，更新未安装")
	}

	didReplace := false
	originalVersion := currentCageVersion(plan.Target)
	if err := replaceApp(plan, requestAuth, &didReplace); err != nil {
		recoverApp(plan, originalVersion, didReplace, requestAuth, err.Error())
		return nil
	}

	token := uuid.NewString()
	env := inheritedProfileEnv()
	env["CAGE_UPDATE_CONFIRMATION"] = token
	pid, err := macnative.LaunchAppWithEnv(plan.Target, env)
	if err != nil {
		recoverApp(plan, originalVersion, didReplace, requestAuth, "新版本未能启动，正在恢复原应用")
		return nil
	}

	ackPath := filepath.Join(filepath.Dir(plan.ResultPath), "launch-"+token+".json")
	ackDeadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(ackDeadline) {
		if data, err := os.ReadFile(ackPath); err == nil && len(data) < 1024 {
			var ack map[string]string
			if json.Unmarshal(data, &ack) == nil && ack["version"] == plan.Version {
				_ = os.Remove(ackPath)
				recordResult(plan, true, fmt.Sprintf("Cage 已更新至 %s", plan.Version))
				_ = os.RemoveAll(filepath.Dir(plan.Backup))
				return nil
			}
		}
		_, terminated, ok := macnative.RunningAppInfo(pid)
		if !ok || terminated {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	if proc, err := os.FindProcess(pid); err == nil {
		_ = proc.Signal(os.Interrupt)
	}
	termDeadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(termDeadline) {
		_, terminated, ok := macnative.RunningAppInfo(pid)
		if !ok || terminated {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if _, terminated, ok := macnative.RunningAppInfo(pid); !ok || terminated {
		recoverApp(plan, originalVersion, didReplace, requestAuth, "新版本工作台未能就绪，正在恢复原应用")
	} else {
		recordResult(plan, false, "新版本未确认就绪，请退出 Cage 后重试；原应用备份已保留")
	}
	return nil
}

func readPlan(rawPath string) (*macnative.UpdatePlan, error) {
	loc, err := filepath.EvalSymlinks(rawPath)
	if err != nil {
		return nil, errors.New("安装任务无效")
	}
	dir := filepath.Dir(loc)
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, errors.New("安装任务目录权限无效")
	}
	stat, ok := info.Sys().(*unix.Stat_t)
	if !ok || int(stat.Uid) != os.Getuid() || info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("安装任务目录权限无效")
	}
	data, err := os.ReadFile(loc)
	if err != nil || len(data) > 16384 {
		return nil, errors.New("安装任务过大")
	}
	var plan macnative.UpdatePlan
	if err := json.Unmarshal(data, &plan); err != nil {
		return nil, errors.New("安装任务无效")
	}
	if int(plan.ParentPID) != os.Getppid() {
		return nil, errors.New("安装任务不属于当前 Cage 应用")
	}
	runningBundle, _, ok := macnative.RunningAppInfo(int(plan.ParentPID))
	if !ok || runningBundle != plan.Target {
		return nil, errors.New("安装任务不属于当前 Cage 应用")
	}
	selfPath, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		return nil, errors.New("安装任务路径或签名不匹配")
	}
	ownTeam, err := macnative.SigningInfo(selfPath, false, "")
	if err != nil || ownTeam != plan.Team {
		return nil, errors.New("安装任务路径或签名不匹配")
	}
	candPath, _ := filepath.EvalSymlinks(plan.Candidate)
	if candPath != filepath.Join(dir, "Cage.app") ||
		plan.Backup != filepath.Join(dir, "previous.app") ||
		plan.ResultPath != filepath.Join(filepath.Dir(dir), "last-result.json") ||
		filepath.Dir(selfPath) != dir {
		return nil, errors.New("安装任务路径或签名不匹配")
	}
	targetResolved, _ := filepath.EvalSymlinks(plan.Target)
	if targetResolved != plan.Target || filepath.Ext(plan.Target) != ".app" || strings.HasPrefix(plan.Target, "/Volumes/") {
		return nil, errors.New("安装位置不支持自动更新")
	}
	if _, err := macnative.SigningInfo(plan.Target, true, ownTeam); err != nil {
		return nil, err
	}
	if err := macnative.VerifyAppBundle(context.Background(), plan.Candidate, plan.Version, ownTeam, true); err != nil {
		return nil, err
	}
	currentVer := currentCageVersion(plan.Target)
	if !macnative.IsNewerVersion(plan.Version, currentVer) {
		return nil, errors.New("更新版本必须高于当前安装版本")
	}
	return &plan, nil
}

func currentCageVersion(appPath string) string {
	data, err := os.ReadFile(filepath.Join(appPath, "Contents", "Info.plist"))
	if err != nil {
		return ""
	}
	info, err := macnative.ParsePlist(data)
	if err != nil {
		return ""
	}
	ver, _ := info["CageVersion"].(string)
	return ver
}

func replaceApp(plan *macnative.UpdatePlan, requestAuth bool, didReplace *bool) error {
	if _, err := macnative.SigningInfo(plan.Target, true, plan.Team); err != nil {
		return err
	}
	if err := macnative.VerifyAppBundle(context.Background(), plan.Candidate, plan.Version, plan.Team, true); err != nil {
		return err
	}
	if err := macnative.ReplaceItemAt(plan.Target, plan.Candidate, requestAuth); err != nil {
		return err
	}
	*didReplace = true
	if err := macnative.VerifyAppBundle(context.Background(), plan.Target, plan.Version, plan.Team, true); err != nil {
		return err
	}
	return nil
}

func recoverApp(plan *macnative.UpdatePlan, originalVersion string, didReplace, requestAuth bool, reason string) {
	msg := reason
	canOpen := false
	originalIntact := macnative.VerifyAppBundle(context.Background(), plan.Target, originalVersion, plan.Team, false) == nil
	if didReplace || !originalIntact {
		if err := macnative.VerifyAppBundle(context.Background(), plan.Backup, originalVersion, plan.Team, false); err != nil {
			msg += fmt.Sprintf("。自动恢复未完成，未启动未经验证的应用。备份位置：%s", plan.Backup)
		} else {
			control := filepath.Join(plan.Backup, "Contents", "Resources", "cage")
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			out, err := exec.CommandContext(ctx, control, "prepare-update", "--json").Output()
			cancel()
			var handoff map[string]any
			if err != nil || json.Unmarshal(out, &handoff) != nil || handoff["status"] != "prepared" {
				msg += fmt.Sprintf("。自动恢复未完成，未启动未经验证的应用。备份位置：%s", plan.Backup)
			} else {
				if !originalIntact {
					if err := macnative.RestoreAppBundle(plan.Backup, plan.Target, requestAuth); err != nil {
						msg += fmt.Sprintf("。自动恢复未完成，未启动未经验证的应用。备份位置：%s", plan.Backup)
						recordResult(plan, false, msg)
						return
					}
				}
				if err := macnative.VerifyAppBundle(context.Background(), plan.Target, originalVersion, plan.Team, false); err != nil {
					msg += fmt.Sprintf("。自动恢复未完成，未启动未经验证的应用。备份位置：%s", plan.Backup)
				} else {
					msg += "。已恢复原应用，可稍后重试"
					canOpen = true
				}
			}
		}
	} else {
		msg += "。原应用已保留"
		canOpen = true
	}
	recordResult(plan, false, msg)
	if canOpen {
		_, _ = macnative.LaunchAppWithEnv(plan.Target, inheritedProfileEnv())
	}
}

func inheritedProfileEnv() map[string]string {
	if p := os.Getenv("CLAUDE_CAGE_CONFIG"); p != "" {
		return map[string]string{"CLAUDE_CAGE_CONFIG": p}
	}
	return map[string]string{}
}

func recordResult(plan *macnative.UpdatePlan, success bool, message string) {
	data, err := json.Marshal(map[string]any{
		"success": success,
		"message": message,
		"version": plan.Version,
	})
	if err != nil {
		return
	}
	_ = macnative.WritePrivateFile(plan.ResultPath, data)
}

func sendJSON(v any) {
	if data, err := json.Marshal(v); err == nil {
		_, _ = fmt.Fprintln(os.Stdout, string(data))
	}
}
