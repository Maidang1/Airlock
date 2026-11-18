//go:build darwin && cgo

package macnative

/*
#cgo CFLAGS: -fobjc-arc -mmacosx-version-min=14.0
#cgo LDFLAGS: -framework Cocoa -framework Security
#include <stdlib.h>
#include "native_darwin.h"
*/
import "C"
import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/google/uuid"
)

// UpdatePlan is written by AppUpdater for CageUpdateInstaller.
type UpdatePlan struct {
	Version    string `json:"version"`
	Team       string `json:"team"`
	Target     string `json:"target"`
	Candidate  string `json:"candidate"`
	Backup     string `json:"backup"`
	ParentPID  int32  `json:"parentPID"`
	ResultPath string `json:"resultPath"`
}

var (
	semverRe  = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)
	sha256Re  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	minSysRe  = regexp.MustCompile(`^[0-9]+(\.[0-9]+){0,2}$`)
	digitsRe  = regexp.MustCompile(`^[0-9]+$`)
)

// IsNewerVersion returns true if candidate is strictly newer than current per semver.
func IsNewerVersion(candidate, current string) bool {
	parse := func(text string) ([3]int, []string, bool) {
		var nums [3]int
		if !semverRe.MatchString(text) {
			return nums, nil, false
		}
		core, preStr, hasPre := strings.Cut(text, "-")
		parts := strings.Split(core, ".")
		if len(parts) != 3 {
			return nums, nil, false
		}
		for i := range 3 {
			v, err := strconv.Atoi(parts[i])
			if err != nil {
				return nums, nil, false
			}
			nums[i] = v
		}
		var pre []string
		if hasPre {
			pre = strings.Split(preStr, ".")
			for _, p := range pre {
				if len(p) > 1 && p[0] == '0' && digitsRe.MatchString(p) {
					return nums, nil, false
				}
			}
		}
		return nums, pre, true
	}
	a, ap, okA := parse(candidate)
	b, bp, okB := parse(current)
	if !okA || !okB {
		return false
	}
	for i := range 3 {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	if len(ap) == 0 || len(bp) == 0 {
		return len(ap) == 0 && len(bp) > 0
	}
	for i := 0; i < len(ap) && i < len(bp); i++ {
		if ap[i] == bp[i] {
			continue
		}
		xn := digitsRe.MatchString(ap[i])
		yn := digitsRe.MatchString(bp[i])
		if xn && yn {
			if len(ap[i]) != len(bp[i]) {
				return len(ap[i]) > len(bp[i])
			}
			return ap[i] > bp[i]
		}
		if xn != yn {
			return !xn
		}
		return ap[i] > bp[i]
	}
	return len(ap) > len(bp)
}

// SigningInfo validates Apple Developer ID code signing on an app bundle or binary.
func SigningInfo(path string, isBundle bool, expectedTeam string) (string, error) {
	cp := C.CString(path)
	cb := C.CString(BundleID)
	ct := C.CString(expectedTeam)
	defer C.free(unsafe.Pointer(cp))
	defer C.free(unsafe.Pointer(cb))
	defer C.free(unsafe.Pointer(ct))

	b := 0
	if isBundle {
		b = 1
	}
	var outTeam *C.char
	var outErr *C.char
	rc := C.cage_signing_info(cp, C.int(b), cb, ct, &outTeam, &outErr)
	if outErr != nil {
		defer C.free(unsafe.Pointer(outErr))
	}
	if outTeam != nil {
		defer C.free(unsafe.Pointer(outTeam))
	}
	if rc != 0 {
		if outErr != nil {
			return "", errors.New(C.GoString(outErr))
		}
		return "", errors.New("更新应用的 Apple 签名无效或发布者不匹配")
	}
	return C.GoString(outTeam), nil
}

// ReplaceItemAt atomically replaces targetPath with candidatePath using NSFileManager.
func ReplaceItemAt(targetPath, candidatePath string, requestAuth bool) error {
	ct := C.CString(targetPath)
	cc := C.CString(candidatePath)
	defer C.free(unsafe.Pointer(ct))
	defer C.free(unsafe.Pointer(cc))
	auth := 0
	if requestAuth {
		auth = 1
	}
	var outErr *C.char
	rc := C.cage_replace_item_at(ct, cc, C.int(auth), &outErr)
	if outErr != nil {
		defer C.free(unsafe.Pointer(outErr))
	}
	if rc != 0 {
		if outErr != nil {
			return errors.New(C.GoString(outErr))
		}
		return errors.New("应用替换失败")
	}
	return nil
}

// LaunchAppWithEnv opens an .app bundle via NSWorkspace with custom environment variables.
func LaunchAppWithEnv(appPath string, env map[string]string) (int, error) {
	envBytes, err := json.Marshal(env)
	if err != nil {
		return 0, err
	}
	ca := C.CString(appPath)
	ce := C.CString(string(envBytes))
	defer C.free(unsafe.Pointer(ca))
	defer C.free(unsafe.Pointer(ce))
	var pid C.int
	var outErr *C.char
	rc := C.cage_launch_app_with_env(ca, ce, &pid, &outErr)
	if outErr != nil {
		defer C.free(unsafe.Pointer(outErr))
	}
	if rc != 0 {
		if outErr != nil {
			return 0, errors.New(C.GoString(outErr))
		}
		return 0, errors.New("无法启动应用程序")
	}
	return int(pid), nil
}

// RunningAppInfo queries NSRunningApplication for a PID.
func RunningAppInfo(pid int) (bundlePath string, terminated bool, ok bool) {
	var outPath *C.char
	var outTerm C.int
	rc := C.cage_running_app_info(C.int(pid), &outPath, &outTerm)
	if outPath != nil {
		bundlePath = C.GoString(outPath)
		C.free(unsafe.Pointer(outPath))
	}
	return bundlePath, outTerm != 0, rc == 0
}

// PrivateDir creates a directory with 0700 permissions and validates it is not a symlink.
func PrivateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return errors.New("更新目录无效")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("更新目录无效")
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return errors.New("更新目录无效")
	}
	return nil
}

// WritePrivateFile atomically writes data to path with 0600 permissions.
func WritePrivateFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func FileSHA256(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 1<<20)
	for {
		if ctx.Err() != nil {
			return "", errors.New("更新验证已取消")
		}
		n, err := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func runTool(ctx context.Context, timeout time.Duration, exe string, args ...string) ([]byte, error) {
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, exe, args...)
	cmd.WaitDelay = 2 * time.Second
	var out bytes.Buffer
	cmd.Stdout = &limitedWriter{buf: &out, max: (1 << 20) + 1}
	cmd.Stderr = &limitedWriter{buf: &out, max: (1 << 20) + 1}
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, errors.New("更新验证已取消")
	}
	if err != nil || out.Len() > (1<<20) {
		return nil, errors.New("本机更新验证未完成，请重试")
	}
	return out.Bytes(), nil
}

// ValidateAppTree checks file count, total size, and prevents symlink escapes inside an extracted app.
func ValidateAppTree(ctx context.Context, appPath string) error {
	resolvedBase, err := filepath.EvalSymlinks(appPath)
	if err != nil {
		return errors.New("无法检查更新包内容")
	}
	basePrefix := resolvedBase + string(filepath.Separator)
	var count int
	var total int64
	err = filepath.WalkDir(appPath, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if ctx.Err() != nil {
			return errors.New("更新验证已取消")
		}
		count++
		if count > 50000 {
			return errors.New("更新包包含越界文件或过多文件")
		}
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || (resolved != resolvedBase && !strings.HasPrefix(resolved, basePrefix)) {
			return errors.New("更新包包含越界文件或过多文件")
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			total += info.Size()
			if total > 1024*1024*1024 {
				return errors.New("解包后的应用超过 1 GiB")
			}
		}
		return nil
	})
	return err
}

// VerifyAppBundle validates Info.plist, minimum OS, Developer ID signatures, architecture, and Gatekeeper assessment.
func VerifyAppBundle(ctx context.Context, appPath, expectedVersion, expectedTeam string, gatekeeper bool) error {
	if ctx.Err() != nil {
		return errors.New("更新验证已取消")
	}
	linfo, err := os.Lstat(appPath)
	if err != nil || !linfo.IsDir() || linfo.Mode()&os.ModeSymlink != 0 || filepath.Ext(appPath) != ".app" {
		return errors.New("更新包中缺少有效的 Airlock.app")
	}
	plistRaw, err := os.ReadFile(filepath.Join(appPath, "Contents", "Info.plist"))
	if err != nil {
		return errors.New("更新包的应用身份或版本不匹配")
	}
	info, err := ParsePlist(plistRaw)
	if err != nil {
		return errors.New("更新包的应用身份或版本不匹配")
	}
	if id, _ := info["CFBundleIdentifier"].(string); id != BundleID {
		return errors.New("更新包的应用身份或版本不匹配")
	}
	if ver, _ := info["CageVersion"].(string); ver != expectedVersion {
		return errors.New("更新包的应用身份或版本不匹配")
	}
	exe, _ := info["CFBundleExecutable"].(string)
	if exe != "Airlock" && exe != "Cage" {
		return errors.New("更新包的应用身份或版本不匹配")
	}
	if minOS, _ := info["LSMinimumSystemVersion"].(string); minOS != "" {
		if !minSysRe.MatchString(minOS) || !isMacOSAtLeast(minOS) {
			return errors.New("此更新需要更高版本的 macOS")
		}
	}
	if _, err := SigningInfo(appPath, true, expectedTeam); err != nil {
		return err
	}
	arch := "arm64"
	if runtime.GOARCH == "amd64" {
		arch = "x86_64"
	}
	if _, err := runTool(ctx, 90*time.Second, "/usr/bin/lipo", filepath.Join(appPath, "Contents", "MacOS", exe), "-verify_arch", arch); err != nil {
		return err
	}
	for _, name := range []string{"cage", "mihomo", "CageUpdateInstaller"} {
		nested := filepath.Join(appPath, "Contents", "Resources", name)
		if _, err := os.Stat(nested); err != nil {
			return errors.New("更新包缺少必要的应用组件")
		}
		if _, err := SigningInfo(nested, false, expectedTeam); err != nil {
			return err
		}
	}
	if gatekeeper {
		if _, err := runTool(ctx, 90*time.Second, "/usr/sbin/spctl", "--assess", "--type", "execute", "--verbose=2", appPath); err != nil {
			return err
		}
	}
	if ctx.Err() != nil {
		return errors.New("更新验证已取消")
	}
	return nil
}

func isMacOSAtLeast(required string) bool {
	out, err := exec.Command("/usr/bin/sw_vers", "-productVersion").Output()
	if err != nil {
		return true
	}
	parse := func(s string) [3]int {
		var res [3]int
		for i, p := range strings.Split(strings.TrimSpace(s), ".") {
			if i >= 3 {
				break
			}
			res[i], _ = strconv.Atoi(p)
		}
		return res
	}
	cur := parse(string(out))
	req := parse(required)
	for i := range 3 {
		if cur[i] != req[i] {
			return cur[i] > req[i]
		}
	}
	return true
}

// RestoreAppBundle copies backup to target safely using NSFileManager.
func RestoreAppBundle(backupPath, targetPath string, requestAuth bool) error {
	copyPath := filepath.Join(filepath.Dir(backupPath), "restore-"+uuid.NewString()+".app")
	if _, err := runTool(context.Background(), 120*time.Second, "/usr/bin/ditto", backupPath, copyPath); err != nil {
		return err
	}
	defer os.RemoveAll(copyPath)
	if _, err := os.Stat(targetPath); err == nil {
		return ReplaceItemAt(targetPath, copyPath, requestAuth)
	}
	return os.Rename(copyPath, targetPath)
}

// ExtractVerifiedDMG checks archive SHA-256, mounts the DMG, verifies Cage.app, copies via ditto, and re-verifies.
func ExtractVerifiedDMG(ctx context.Context, archivePath, hash, version, team, directory string) (string, error) {
	actual, err := FileSHA256(ctx, archivePath)
	if err != nil {
		return "", err
	}
	if actual != hash {
		return "", errors.New("安装包下载后发生变化，请重新下载")
	}
	mountPoint := filepath.Join(directory, "mount")
	if err := PrivateDir(mountPoint); err != nil {
		return "", err
	}
	if _, err := runTool(ctx, 90*time.Second, "/usr/bin/hdiutil", "attach", "-readonly", "-nobrowse", "-noautoopen", "-mountpoint", mountPoint, archivePath); err != nil {
		return "", err
	}
	defer func() {
		if _, err := runTool(context.Background(), 30*time.Second, "/usr/bin/hdiutil", "detach", mountPoint); err != nil {
			_, _ = runTool(context.Background(), 30*time.Second, "/usr/bin/hdiutil", "detach", "-force", mountPoint)
		}
	}()
	if ctx.Err() != nil {
		return "", errors.New("更新验证已取消")
	}
	source := filepath.Join(mountPoint, "Airlock.app")
	if _, err := os.Lstat(source); os.IsNotExist(err) {
		source = filepath.Join(mountPoint, "Cage.app")
	}
	if err := ValidateAppTree(ctx, source); err != nil {
		return "", err
	}
	if err := VerifyAppBundle(ctx, source, version, team, true); err != nil {
		return "", err
	}
	target := filepath.Join(directory, filepath.Base(source))
	if _, err := runTool(ctx, 180*time.Second, "/usr/bin/ditto", source, target); err != nil {
		return "", err
	}
	if err := ValidateAppTree(ctx, target); err != nil {
		return "", err
	}
	if err := VerifyAppBundle(ctx, target, version, team, true); err != nil {
		return "", err
	}
	return target, nil
}

type preparedUpdate struct {
	directory string
	app       string
	version   string
	team      string
}

// AppUpdater owns in-app DMG update checking, verification, and staged replacement.
type AppUpdater struct {
	mu           sync.Mutex
	profile      ProfileInfo
	onChange     func(map[string]any)
	quit         func()
	reconnect    func()
	state        map[string]any
	worker       *exec.Cmd
	validation   context.CancelFunc
	validationWG sync.WaitGroup
	installer    *exec.Cmd
	installerIn  io.WriteCloser
	prepared     *preparedUpdate
	generation   int
	launchAcked  bool
	wbReady      bool
	committed    bool
	terminating  bool
}

// NewAppUpdater initializes an AppUpdater from the current ProfileInfo.
func NewAppUpdater(profile ProfileInfo, onChange func(map[string]any), quit, reconnect func()) *AppUpdater {
	state := map[string]any{
		"phase":          "idle",
		"currentVersion": profile.Version,
		"message":        "",
	}
	if data, err := os.ReadFile(filepath.Join(profile.UpdatesDir, "last-result.json")); err == nil && len(data) < 16384 {
		var result map[string]any
		if json.Unmarshal(data, &result) == nil {
			if msg, ok := result["message"].(string); ok {
				state["message"] = msg
			}
			if success, ok := result["success"].(bool); ok && !success {
				state["phase"] = "failed"
				state["retryable"] = true
			}
		}
	}
	return &AppUpdater{
		profile:   profile,
		onChange:  onChange,
		quit:      quit,
		reconnect: reconnect,
		state:     state,
	}
}

func (u *AppUpdater) Snapshot() map[string]any {
	u.mu.Lock()
	defer u.mu.Unlock()
	return maps.Clone(u.state)
}

func (u *AppUpdater) changeLocked(values map[string]any) map[string]any {
	if phase, _ := values["phase"].(string); phase == "current" {
		delete(u.state, "latestVersion")
	}
	maps.Copy(u.state, values)
	snap := maps.Clone(u.state)
	return snap
}

func (u *AppUpdater) emit(snap map[string]any) {
	if u.onChange != nil {
		u.onChange(snap)
	}
}

func (u *AppUpdater) failLocked(msg, retryAt string) map[string]any {
	return u.changeLocked(map[string]any{
		"phase":     "failed",
		"message":   msg,
		"retryable": true,
		"retryAt":   retryAt,
	})
}

func (u *AppUpdater) AwaitingInitialWorkbench() bool {
	token := os.Getenv("CAGE_UPDATE_CONFIRMATION")
	if _, err := uuid.Parse(token); err != nil {
		return false
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	return !u.wbReady
}

func (u *AppUpdater) MarkLaunchReady() {
	u.mu.Lock()
	u.wbReady = true
	if u.launchAcked {
		u.mu.Unlock()
		return
	}
	token := os.Getenv("CAGE_UPDATE_CONFIRMATION")
	if _, err := uuid.Parse(token); err != nil {
		u.mu.Unlock()
		return
	}
	ver, _ := u.state["currentVersion"].(string)
	u.mu.Unlock()

	data, err := json.Marshal(map[string]string{"version": ver})
	if err != nil {
		return
	}
	if err := PrivateDir(u.profile.UpdatesDir); err != nil {
		u.mu.Lock()
		snap := u.failLocked("更新已安装，但启动确认未能保存", "")
		u.mu.Unlock()
		u.emit(snap)
		return
	}
	if err := WritePrivateFile(filepath.Join(u.profile.UpdatesDir, "launch-"+token+".json"), data); err != nil {
		u.mu.Lock()
		snap := u.failLocked("更新已安装，但启动确认未能保存", "")
		u.mu.Unlock()
		u.emit(snap)
		return
	}
	u.mu.Lock()
	u.launchAcked = true
	snap := u.changeLocked(map[string]any{
		"phase":   "current",
		"message": fmt.Sprintf("Airlock 已更新至 %s", ver),
	})
	u.mu.Unlock()
	u.emit(snap)
}

func (u *AppUpdater) Handle(operation string) map[string]any {
	u.mu.Lock()
	if u.profile.IntelMacTest {
		var snap map[string]any
		if u.state["phase"] != "manual" {
			snap = u.changeLocked(map[string]any{"phase": "manual", "message": "Intel 测试版通过独立测试包更新。"})
		} else {
			snap = maps.Clone(u.state)
		}
		u.mu.Unlock()
		u.emit(snap)
		return snap
	}
	if u.profile.MacGuestTrial {
		var snap map[string]any
		if u.state["phase"] != "manual" {
			snap = u.changeLocked(map[string]any{"phase": "manual", "message": "macOS 客机试用版通过本机安装更新。"})
		} else {
			snap = maps.Clone(u.state)
		}
		u.mu.Unlock()
		u.emit(snap)
		return snap
	}
	if u.profile.ClientOnly {
		var snap map[string]any
		if u.state["phase"] != "manual" || operation != "status" {
			snap = u.changeLocked(map[string]any{"phase": "manual", "message": "轻量客户端使用独立安装包更新；连接配置会保留，云端任务不受影响。"})
		} else {
			snap = maps.Clone(u.state)
		}
		u.mu.Unlock()
		u.emit(snap)
		return snap
	}
	if runtime.GOARCH == "amd64" && u.profile.BundleID == BundleID {
		var snap map[string]any
		if u.state["phase"] != "manual" {
			snap = u.changeLocked(map[string]any{"phase": "manual", "message": "Intel 开发构建通过独立安装包更新。"})
		} else {
			snap = maps.Clone(u.state)
		}
		u.mu.Unlock()
		u.emit(snap)
		return snap
	}
	phase, _ := u.state["phase"].(string)
	busy := phase == "checking" || phase == "downloading" || phase == "downloaded" || phase == "verifying" || phase == "installing"
	if operation == "check" || operation == "install" {
		if rawRetry, _ := u.state["retryAt"].(string); rawRetry != "" {
			if retryAt, err := time.Parse(time.RFC3339, rawRetry); err == nil && retryAt.After(time.Now()) {
				snap := maps.Clone(u.state)
				u.mu.Unlock()
				return snap
			}
		}
	}

	switch operation {
	case "status":
		snap := maps.Clone(u.state)
		u.mu.Unlock()
		return snap
	case "check":
		if !busy && u.worker == nil && u.validation == nil && u.installer == nil && u.prepared == nil {
			u.startDownloadLocked(true)
		}
	case "install":
		if !busy && u.worker == nil && u.validation == nil && u.installer == nil {
			if u.prepared != nil {
				u.beginInstallationLocked()
			} else {
				u.startDownloadLocked(false)
			}
		}
	case "cancel":
		if u.installer == nil && phase != "installing" {
			u.generation++
			stopCmdGracefully(u.worker)
			if u.validation != nil {
				u.validation()
			}
			nextPhase := "available"
			if u.prepared != nil {
				nextPhase = "ready"
			}
			snap := u.changeLocked(map[string]any{
				"phase":     nextPhase,
				"message":   "更新已暂停，可稍后继续",
				"retryable": true,
			})
			u.mu.Unlock()
			u.emit(snap)
			return snap
		}
	}
	snap := maps.Clone(u.state)
	u.mu.Unlock()
	return snap
}

func (u *AppUpdater) CancelForTermination() {
	u.mu.Lock()
	u.generation++
	stopCmdGracefully(u.worker)
	if u.validation != nil {
		u.validation()
	}
	if u.installerIn != nil {
		_ = u.installerIn.Close()
		u.installerIn = nil
	}
	if !u.committed {
		stopCmdGracefully(u.installer)
	}
	u.mu.Unlock()
}

// PrepareForTermination cancels active download/validation/install work before app exit.
// It returns true if cleanup was needed and invokes done asynchronously once clean.
func (u *AppUpdater) PrepareForTermination(done func()) bool {
	u.mu.Lock()
	if u.committed {
		u.mu.Unlock()
		return false
	}
	if u.terminating {
		u.mu.Unlock()
		return true
	}
	hasWork := u.worker != nil || u.validation != nil || u.installer != nil || u.prepared != nil
	if !hasWork {
		u.mu.Unlock()
		return false
	}
	u.terminating = true
	worker := u.worker
	helper := u.installer
	var prepDir string
	if u.prepared != nil {
		prepDir = u.prepared.directory
	}
	u.generation++
	stopCmdGracefully(worker)
	if u.validation != nil {
		u.validation()
	}
	if u.installerIn != nil {
		_ = u.installerIn.Close()
		u.installerIn = nil
	}
	stopCmdGracefully(helper)
	snap := u.changeLocked(map[string]any{"message": "正在取消更新并清理临时文件…"})
	u.mu.Unlock()
	u.emit(snap)

	go func() {
		if worker != nil {
			_ = worker.Wait()
		}
		u.validationWG.Wait()
		if helper != nil {
			_ = helper.Wait()
		}
		if prepDir != "" {
			_ = os.RemoveAll(prepDir)
		}
		u.mu.Lock()
		u.terminating = false
		u.mu.Unlock()
		if done != nil {
			done()
		}
	}()
	return true
}

func (u *AppUpdater) startDownloadLocked(checkOnly bool) {
	u.generation++
	op := u.generation
	snap := u.changeLocked(map[string]any{
		"phase":         "checking",
		"message":       "正在检查更新，优先使用主机网络",
		"receivedBytes": 0,
		"totalBytes":    0,
		"retryable":     false,
		"retryAt":       "",
	})
	u.emit(snap)

	cageBin, err := CageBinaryPath()
	if err != nil {
		snap = u.failLocked("无法启动更新检查，请确认应用组件完整", "")
		u.emit(snap)
		return
	}
	sub := "download"
	if checkOnly {
		sub = "check"
	}
	cmd := exec.Command(cageBin, "app-update", sub)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		snap = u.failLocked("无法启动更新检查，请确认应用组件完整", "")
		u.emit(snap)
		return
	}
	if err := cmd.Start(); err != nil {
		snap = u.failLocked("无法启动更新检查，请确认应用组件完整", "")
		u.emit(snap)
		return
	}
	u.worker = cmd

	go func() {
		scanner := bufio.NewScanner(io.LimitReader(stdout, 65536))
		var last map[string]any
		for scanner.Scan() {
			var val map[string]any
			if json.Unmarshal(scanner.Bytes(), &val) != nil {
				continue
			}
			last = val
			visible := maps.Clone(val)
			delete(visible, "archivePath")
			delete(visible, "sha256")
			u.mu.Lock()
			if u.generation == op {
				s := u.changeLocked(visible)
				u.mu.Unlock()
				u.emit(s)
			} else {
				u.mu.Unlock()
			}
		}
		waitErr := cmd.Wait()
		u.mu.Lock()
		if u.worker == cmd {
			u.worker = nil
		}
		if u.generation != op {
			u.mu.Unlock()
			return
		}
		if waitErr != nil {
			msg, _ := last["message"].(string)
			if msg == "" {
				msg = "更新请求未完成，请重试"
			}
			retryAt, _ := last["retryAt"].(string)
			s := u.failLocked(msg, retryAt)
			u.mu.Unlock()
			u.emit(s)
			return
		}
		if !checkOnly && last != nil && last["phase"] == "downloaded" {
			u.verifyDownloadLocked(last, op)
			u.mu.Unlock()
			return
		}
		u.mu.Unlock()
	}()
}

func (u *AppUpdater) verifyDownloadLocked(result map[string]any, op int) {
	rawPath, _ := result["archivePath"].(string)
	hash, _ := result["sha256"].(string)
	ver, _ := result["latestVersion"].(string)
	if rawPath == "" || ver == "" || !sha256Re.MatchString(hash) {
		s := u.failLocked("下载进程返回了无效安装包", "")
		u.emit(s)
		return
	}
	archiveResolved, err := filepath.EvalSymlinks(rawPath)
	if err != nil {
		archiveResolved = filepath.Clean(rawPath)
	}
	rootResolved, err := filepath.EvalSymlinks(u.profile.UpdatesDir)
	if err != nil {
		rootResolved = filepath.Clean(u.profile.UpdatesDir)
	}
	if !strings.HasPrefix(archiveResolved, rootResolved+"/download-") || (filepath.Base(archiveResolved) != "Airlock.dmg" && filepath.Base(archiveResolved) != "Cage.dmg") {
		s := u.failLocked("安装包路径不属于本次私有下载", "")
		u.emit(s)
		return
	}
	if u.profile.BundlePath == "" {
		s := u.failLocked("请先将 Airlock 安装到应用程序目录，再自动更新", "")
		u.emit(s)
		return
	}

	s := u.changeLocked(map[string]any{
		"phase":     "verifying",
		"message":   "正在验证 Apple 签名和应用内容",
		"retryable": false,
	})
	u.emit(s)

	ctx, cancel := context.WithCancel(context.Background())
	u.validation = cancel
	u.validationWG.Add(1)
	go func() {
		defer u.validationWG.Done()
		defer func() {
			cancel()
			u.mu.Lock()
			u.validation = nil
			u.mu.Unlock()
		}()
		dir := filepath.Join(u.profile.UpdatesDir, "verified-"+uuid.NewString())
		cleanArchive := func() { _ = os.RemoveAll(filepath.Dir(archiveResolved)) }
		team, err := SigningInfo(u.profile.BundlePath, true, "")
		if err == nil {
			err = PrivateDir(dir)
		}
		var app string
		if err == nil {
			app, err = ExtractVerifiedDMG(ctx, archiveResolved, hash, ver, team, dir)
		}
		cleanArchive()
		if err != nil {
			_ = os.RemoveAll(dir)
			u.mu.Lock()
			if u.generation == op {
				fail := u.failLocked(err.Error(), "")
				u.mu.Unlock()
				u.emit(fail)
				return
			}
			u.mu.Unlock()
			return
		}
		u.mu.Lock()
		if u.generation != op {
			u.mu.Unlock()
			_ = os.RemoveAll(dir)
			return
		}
		u.prepared = &preparedUpdate{directory: dir, app: app, version: ver, team: team}
		readySnap := u.changeLocked(map[string]any{
			"phase":     "ready",
			"message":   "更新已下载并验证",
			"retryable": true,
		})
		u.emit(readySnap)
		u.beginInstallationLocked()
		u.mu.Unlock()
	}()
}

func (u *AppUpdater) beginInstallationLocked() {
	if u.prepared == nil || u.installer != nil {
		return
	}
	prep := u.prepared
	op := u.generation
	s := u.changeLocked(map[string]any{
		"phase":     "installing",
		"message":   "正在准备安装更新",
		"retryable": false,
	})
	u.emit(s)

	ctx, cancel := context.WithCancel(context.Background())
	u.validation = cancel
	u.validationWG.Add(1)
	go func() {
		defer u.validationWG.Done()
		defer func() {
			cancel()
			u.mu.Lock()
			u.validation = nil
			u.mu.Unlock()
		}()

		bundleResolved, err := filepath.EvalSymlinks(u.profile.BundlePath)
		if err != nil || bundleResolved == "" || strings.HasPrefix(bundleResolved, "/Volumes/") || filepath.Ext(bundleResolved) != ".app" {
			u.emitFailureForOp(op, "请先将 Airlock 安装到应用程序目录，再自动更新")
			return
		}
		if err := VerifyAppBundle(ctx, prep.app, prep.version, prep.team, true); err != nil {
			u.emitFailureForOp(op, err.Error())
			return
		}
		origHelper := filepath.Join(bundleResolved, "Contents", "Resources", "CageUpdateInstaller")
		helper := filepath.Join(prep.directory, "CageUpdateInstaller")
		_ = os.Remove(helper)
		if _, err := runTool(ctx, 30*time.Second, "/bin/cp", origHelper, helper); err != nil {
			u.emitFailureForOp(op, err.Error())
			return
		}
		if _, err := SigningInfo(helper, false, prep.team); err != nil {
			u.emitFailureForOp(op, err.Error())
			return
		}
		plan := UpdatePlan{
			Version:    prep.version,
			Team:       prep.team,
			Target:     bundleResolved,
			Candidate:  prep.app,
			Backup:     filepath.Join(prep.directory, "previous.app"),
			ParentPID:  int32(os.Getpid()),
			ResultPath: filepath.Join(u.profile.UpdatesDir, "last-result.json"),
		}
		planBytes, err := json.Marshal(plan)
		if err != nil {
			u.emitFailureForOp(op, err.Error())
			return
		}
		planPath := filepath.Join(prep.directory, "install.json")
		if err := WritePrivateFile(planPath, planBytes); err != nil {
			u.emitFailureForOp(op, err.Error())
			return
		}
		if ctx.Err() != nil {
			u.emitFailureForOp(op, "更新验证已取消")
			return
		}
		u.mu.Lock()
		if u.generation == op {
			u.launchInstallerLocked(helper, planPath)
		}
		u.mu.Unlock()
	}()
}

func (u *AppUpdater) emitFailureForOp(op int, msg string) {
	u.mu.Lock()
	if u.generation != op {
		u.mu.Unlock()
		return
	}
	s := u.failLocked(msg, "")
	u.mu.Unlock()
	u.emit(s)
}

func (u *AppUpdater) launchInstallerLocked(helper, planPath string) {
	cmd := exec.Command(helper, planPath)
	inPipe, err := cmd.StdinPipe()
	if err != nil {
		u.emit(u.failLocked("无法启动安全安装程序", ""))
		return
	}
	outPipe, err := cmd.StdoutPipe()
	if err != nil {
		_ = inPipe.Close()
		u.emit(u.failLocked("无法启动安全安装程序", ""))
		return
	}
	if err := cmd.Start(); err != nil {
		_ = inPipe.Close()
		u.emit(u.failLocked("无法启动安全安装程序", ""))
		return
	}
	u.installer = cmd
	u.installerIn = inPipe

	go func() {
		scanner := bufio.NewScanner(io.LimitReader(outPipe, 16384))
		var resp map[string]any
		if scanner.Scan() {
			_ = json.Unmarshal(scanner.Bytes(), &resp)
		}
		u.mu.Lock()
		if u.installer != cmd || u.terminating {
			u.mu.Unlock()
			return
		}
		if status, _ := resp["status"].(string); status == "ready" {
			u.mu.Unlock()
			u.prepareEnvironment(false)
			return
		}
		msg, _ := resp["message"].(string)
		if msg == "" {
			msg = "安全安装准备失败，原应用已保留"
		}
		u.abortInstallerLocked()
		s := u.failLocked(msg, "")
		u.mu.Unlock()
		u.emit(s)
	}()
}

func (u *AppUpdater) abortInstallerLocked() {
	if u.installerIn != nil {
		_ = u.installerIn.Close()
		u.installerIn = nil
	}
	if helper := u.installer; helper != nil {
		stopCmdGracefully(helper)
		go func() {
			_ = helper.Wait()
			u.mu.Lock()
			if u.installer == helper {
				u.installer = nil
			}
			u.mu.Unlock()
		}()
	}
}

func (u *AppUpdater) prepareEnvironment(stop bool) {
	msg := "正在检查环境是否可安全更新"
	if stop {
		msg = "正在停止环境，准备安装"
	}
	u.mu.Lock()
	u.emit(u.changeLocked(map[string]any{"phase": "installing", "message": msg}))
	u.mu.Unlock()

	go func() {
		cageBin, err := CageBinaryPath()
		if err != nil {
			u.mu.Lock()
			u.abortInstallerLocked()
			s := u.failLocked(err.Error(), "")
			u.mu.Unlock()
			u.emit(s)
			return
		}
		args := []string{"prepare-update"}
		if stop {
			args = append(args, "--stop")
		}
		args = append(args, "--json")

		ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, cageBin, args...)
		var out bytes.Buffer
		cmd.Stdout = &limitedWriter{buf: &out, max: 16385}
		runErr := cmd.Run()

		var result map[string]any
		if out.Len() > 16384 || json.Unmarshal(out.Bytes(), &result) != nil {
			u.mu.Lock()
			u.abortInstallerLocked()
			s := u.failLocked("无法确认环境已停止", "")
			u.mu.Unlock()
			u.emit(s)
			return
		}

		u.mu.Lock()
		if u.terminating {
			u.mu.Unlock()
			return
		}
		status, _ := result["status"].(string)
		switch {
		case status == "prepared" && runErr == nil:
			if u.installer == nil || u.installer.Process == nil {
				s := u.failLocked("安装程序已退出，原应用已保留", "")
				u.mu.Unlock()
				u.emit(s)
				if u.reconnect != nil {
					u.reconnect()
				}
				return
			}
			_, writeErr := io.WriteString(u.installerIn, "COMMIT\n")
			_ = u.installerIn.Close()
			u.installerIn = nil
			if writeErr != nil {
				u.abortInstallerLocked()
				s := u.failLocked("无法开始安装，原应用已保留", "")
				u.mu.Unlock()
				u.emit(s)
				if u.reconnect != nil {
					u.reconnect()
				}
				return
			}
			u.committed = true
			s := u.changeLocked(map[string]any{"phase": "installing", "message": "正在安装并重启 Airlock"})
			u.mu.Unlock()
			u.emit(s)
			if u.quit != nil {
				u.quit()
			}
		case status == "stop_required" && !stop:
			u.mu.Unlock()
			u.prepareEnvironment(true)
		default:
			errMsg, _ := result["message"].(string)
			if errMsg == "" {
				errMsg = "环境未能安全停止，更新未安装"
			}
			u.abortInstallerLocked()
			s := u.failLocked(errMsg, "")
			u.mu.Unlock()
			u.emit(s)
		}
	}()
}

func stopCmdGracefully(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	proc := cmd.Process
	time.AfterFunc(2*time.Second, func() {
		_ = proc.Signal(syscall.SIGKILL)
	})
}
