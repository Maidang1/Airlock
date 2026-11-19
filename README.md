# Airlock (MyGo macOS Edition)

Airlock 为 macOS 提供安全、可控的隔离环境工作台（基于 [MyGo](https://mygo.egoist.dev/) 原生桌面框架构建，保留 **macOS 主机端全套能力**，并兼容 `~/.claude-cage` 配置与 `cage` 辅助命令）。

## 架构概览

- **原生桌面客户端（纯 Go + Objective-C cgo）**：
  - [`cmd/cage-desktop/`](cmd/cage-desktop/main.go)：MyGo 桌面应用主入口，原生 macOS 菜单栏、单实例锁定、更新生命周期与退出保护，以及 `e2e` UI 自动化挂载点。
  - [`internal/mygoclient/core/`](internal/mygoclient/core/controller.go)：本地/远程控制器会话认证（`cage desktop --json` + `/api/session` + `X-Cage-Connection`）、状态轮询与 SSE、48kHz PCM 音频流（`mygo/audio`）、双向剪贴板同步及 `/api/agent/native` 原生动作循环。
  - [`internal/mygoclient/surfaces/`](internal/mygoclient/surfaces/terminal_surface.go)：
    - Ghostty `libghostty-vt` 原生终端（支持 Claude OAuth 授权链接捕获与拖拽上传）；
    - macOS 客机原生窗口画布（`ui.Image` + pointer/scroll/keyboard + `ui.TextInputClient` 中文输入法合成）；
    - Linux 客机 / Cloud SSH 隔离浏览器的轻量 noVNC WebView 画布（`web-surface/`）。
  - [`internal/mygoclient/views/`](internal/mygoclient/views/workbench_view.go)：原生工作台、账户锁定/创建/Touch ID、初始化向导、代理（Mihomo 订阅/链式路由/测速/连接审计）、安全门控、Claude 多账号额度调度、Tailscale、Cloud SSH 远程主机、密码保险箱/扩展/下载及手机配对二维码管理。
  - [`internal/macnative/`](internal/macnative/profile.go) & [`cmd/cage-update-installer/`](cmd/cage-update-installer/main.go)：macOS Keychain 快捷解锁（Touch ID / 记住登录 / 试用客机密码）、`NSPasteboard` 剪贴板读写与文件缓存清理、`codesign` 校验及原子 `.app` 更新安装器。
- **隔离后端与客机能力（复用完整 macOS / Linux / Cloud 后端）**：
  - Apple Virtualization 隔离 macOS 客机与 Lima Linux 客机（`cmd/cage`）
  - Cloud SSH 远程 Linux / Windows Server 工作台与轻量客户端（`cmd/cage-client`, `deploy/cloud`, `deploy/relay`, `tools/tailnet`）

## 环境要求

- Apple Silicon macOS (macOS 14+)
- Xcode / Command Line Tools（用于 Swift / Objective-C 编译及 `codesign`）
- Go（`GOTOOLCHAIN=auto`，依赖 Go 1.27.4+ 与 `github.com/egoist/mygo v0.9.1`）
- Node.js 20+（用于构建与校验 `web-surface/` 资源清单）
- Python 3（用于 `novnc-patch.py` 等辅助脚本）

## 常用开发与构建命令

```bash
# 1. 运行全量 Go 测试（含 headless MyGo ui.NewTester 交互测试、macnative、workbench 与 tailnet）
GOTOOLCHAIN=auto make test-go

# 2. 启动桌面开发模式
GOTOOLCHAIN=auto make dev

# 3. 打包签名完整的 Airlock.app / Airlock.dmg 与轻量 Airlock Client.app
GOTOOLCHAIN=auto make build-app
GOTOOLCHAIN=auto make dmg
GOTOOLCHAIN=auto make build-client
```

### 使用本地 MicroVM 镜像开发

```bash
# 首次准备镜像，或修改 airlock-init / 镜像依赖后重新构建（需要 Docker）
make microvm-image

# 已有 _output/microvm-image 时，直接启动原生 App 开发模式
make dev-microvm
```

`dev-microvm` 使用 Apple Virtualization 直接加载 `vmlinuz`、`initrd.img`、`rootfs.img` 和 `data.img`，打开独立的 MicroVM 开发版 App。首次使用先在 App 中创建本地账户，再点击“启动环境”；在工作台左上角“添加项目”中选择共享目录。镜像构建不接收项目路径，也不需要 `--project /path/to/your/project`。

镜像默认安装 Node.js 22、npm 和 Claude Code。运行配置保存在 `~/.claude-cage-microvm-dev/`，每个配置使用自己的可写数据盘；用户主目录中的文件和登录状态随数据盘保留。未配置代理时可使用离线终端和目录共享，外网需配置代理并通过现有检查后开启。

```bash
# 可选：使用另一套镜像或独立开发配置
AIRLOCK_MICROVM_IMAGE_DIR=/absolute/path/to/microvm-image \
AIRLOCK_MICROVM_CONFIG="$HOME/.airlock-another-dev/config.json" \
make dev-microvm
```

MyGo 会监听 App 源码变化。修改后台 `cmd/cage` / `internal/workbench` 后，先在 App 中停止环境，退出开发命令，再运行 `make dev-microvm` 以重建后台。修改客机 init 后还需重新运行 `make microvm-image`；重建镜像前应停止使用该镜像的 VM。

当前 MicroVM 开发入口支持普通/Claude 终端、运行时项目共享、基础浏览器和 VNC。托管 Claude 多账号会话、浏览器扩展/保险箱/下载管理等高级功能尚未接入。

## 验证检查清单

构建完成后可执行以下检查确保虚拟化权限与内嵌资源完整：

```bash
codesign --verify --deep --strict _output/dist/darwin-arm64/Airlock.app
codesign -d --entitlements :- _output/dist/darwin-arm64/Airlock.app/Contents/Resources/cage
_output/dist/darwin-arm64/Airlock.app/Contents/Resources/cage verify-macos-guest
_output/dist/darwin-arm64/Airlock.app/Contents/Resources/cage verify-vm-assets
_output/dist/darwin-arm64/Airlock.app/Contents/Resources/cage verify-web-assets
codesign --verify --deep --strict "_output/client-dist/darwin-arm64/Airlock Client.app"
```
