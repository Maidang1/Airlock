# 交接：移除 Lima Linux，本地 Linux 只走 MicroVM

状态：**已提交 `f7a9f1c`**（基线 `153e740`）。未提交残留见下文"注意"。

## 目标与约定（用户已确认）

- 只删 Lima Linux。保留 macOS 客机（仍基于内置 Lima 的 vz 驱动和 hostagent）、Windows、Cloud SSH 和容器环境（containerenv）。
- 先删，功能后补：MicroVM 上还没移植的功能，入口要明确提示"暂不支持"（**未完成**，见下文"待办"）。
- `make dev` 直接使用 `_output/microvm-image`，`make dev-microvm` 是它的别名。

## 已完成

- **开发脚本**
  - `scripts/dev.sh`：只重建过期的产物，然后运行 `go tool mygo dev`。启动前检查 `vmlinuz`、`rootfs.img`、`data.img` 是否存在。
  - `scripts/dev-microvm.sh` 已删除。
  - Makefile 新增 `cage-bin`；`assets` 只构建 Darwin guest agent 和 templates。
- **删除的包和文件**
  - `cmd/agent-vm`、`agent-vm.sh`、`runtime.example.sh`
  - `internal/cli`、`internal/ui`、`internal/runscript`、`internal/gitguard`、`internal/env`、`internal/gitx`、`internal/vm/vmtest`
  - `internal/mounts` 里的构建和挂载部分
  - `internal/vm/migrate.go`、`internal/vm/graphics.go`
  - `internal/limaembed/host-images/ubuntu-22.04.yaml`（share.go 的 `//go:embed` 指令随 `ReadImageTemplate` 删除，文件当时漏删，后补删）
  - `internal/workbench` 里的 `linux_guest_image.go`、`transfer_mount.go`
  - 以及这些代码对应的测试和 bashref JSON
- **`cmd/cage`**：Lima Linux 子命令（`setup`、`up`、`shell`、`run`、`claude`、`list`、`doctor` 等）改为报错，提示改用 Airlock app 里的 MicroVM。`version`、`licenses`、`stop`、`check` 改为本地实现。
- **`internal/vm`**：`Create` 和 `Start` 只接受 DARWIN 模板；去掉了 mounts、Clone 等 Linux 专用逻辑。`NoContainerd` 保留，因为 macOS 安装会用到它。
- **`internal/workbench`**
  - 新增 `errLimaLinuxRemoved`。既不是 MicroVM、macOS、Windows，也不是容器环境的旧 profile，在 `attach`、`startEnvironment` 和 `/api/start` 都会被这个错误拒绝。
  - `vmAlive` 改为函数变量（测试接缝），现在只识别容器环境。
  - `linux_guest_fixture_test.go` 在测试里模拟旧的运行记录，供依赖"VM 运行中"的测试使用。
- **其他包**：精简了 `internal/cage`、`guestos`、`hostimage`、`host`、`paths`、`state`、`vmname`。`hostimage` 的 Linux catalog、`IsLinux` 和 `ClearLinuxTemplate` 保留，用来识别旧记录。
- **测试修正**：本轮改了 3 个只断言 Lima Linux 行为的测试，让它们断言新行为：
  - `guest_system_start_test.go`：间接启动 Linux 必须报错；
  - `initialization_test.go`：没有安装客机的 profile 必须走初始化；
  - `running_record_test.go`：只保留 `macos` 子用例。
- **MicroVM"暂不支持"提示（原待办 1，已完成）**
  - 新增 `internal/workbench/microvm_routes.go`：`microVMUnsupportedRoute` 屏蔽 `/api/claude-desktop*`、`/api/vault/browser`、`/api/browser/extensions/upload`、`/api/browser/files/uploads|downloads/…`，以及 `/api/claude-accounts` 的非 GET 请求；GET 面板路由保持 200，让 UI 能解释现状（`/api/browser/files/saved` 是纯宿主侧实现，未屏蔽）。
  - `server.go` 中间件在 macOS gate 之后、beginActivity 之前统一返回 409"此功能尚未接入 MicroVM 开发环境"；放在认证之后，未认证仍是 401。
  - `claude_accounts_discovery.go`：MicroVM 下账号面板 `DetectionStatus=unsupported`，说明"请在终端内直接登录 Claude"；`pollClaudeAccounts` 跳过后台轮询。
  - 顺带修复：`/api/start` 遇到旧 Lima Linux 安装记录时原来提示"请继续原安装"（一个不可能完成的安装），现在返回 `errLimaLinuxRemoved`。
  - 测试：新增 `microvm_routes_test.go`；`guest_system_start_test.go` 新增 `TestLegacyLimaLinuxProfileIsRefused`。

## 验证情况

- 在 Linux（Go 1.27.4）上，`go vet -tags yq_noxml ./internal/... ./cmd/...` 只剩 `internal/mygoclient` 报错。它只能在 darwin 上构建，基线就是这样。
- 在 darwin/arm64 上，`cageclient` 标签可以构建通过。完整的 darwin 构建需要 cgo（vz、macnative），**本机无法验证**。
- Linux 全量测试只剩存储类失败，报错都是"请选择支持私有权限和稀疏文件的本地磁盘"，基线上同样失败，是环境问题。
  - 涉及包：`internal/cage`、`guestos`、`vm`、`workbench`。
  - 其中 `TestControllerNamesFollowIndependentProfileBudgets` 也是这个原因。
- `managedproxy` 的 `TestLinuxCoreStopPinsAndChecksProcessIdentity` 偶发失败，单独运行能通过，和本次改动无关。
- **没有在 macOS 上实际运行过** `make dev`、macOS 客机或 MicroVM。
- **后续会话已在 darwin/arm64（本机）验证**：`go vet`（含 Makefile 标签）通过，包括 `internal/mygoclient`；`make cage-bin` cgo 构建（vz+macnative）+ codesign 通过；`cage` 子命令冒烟测试通过（up/shell/run 等报"改用 MicroVM"提示，exit 1）；workbench 全量 **1085 个测试通过**；旧 Lima profile 的拒绝行为已由 `TestLegacyLimaLinuxProfileIsRefused` 覆盖。
- `go mod tidy` 把 `containerd/continuity` 归入 indirect（只剩 third_party/lima 直接引用），`go.sum` 现与基线完全一致。

## 待办

1. ~~**MicroVM 上的"暂不支持"提示（必须做）**~~ ✅ 已完成，见"已完成"小节。
2. **在 macOS 上实测**（部分完成：darwin 构建、冒烟测试、旧 profile 拒绝测试已过；**剩余需要人来操作**）
   - `make microvm-image && make dev`（Docker daemon 未起，`_output/microvm-image` 已有可用产物，可直接 `make dev`；GUI 需人交互）；
   - macOS 客机的创建和启动（需要 IPSW 与 GUI）。
3. ~~**重新生成 `THIRD_PARTY_LICENSES`**~~ ✅ 已重新生成（98 modules，移除了 cilium/ebpf、mdlayher/netlink 等 Linux-only 依赖条目）。
4. **检查 `docs/architecture.html` 和 README**：README 已确认无 Lima 残留。`architecture.html` 仍有 Linux 客机（PLANE 4B "Debian 13 + systemd"）、Linux 客机标签页、DNS findings、roadmap 等过时内容，但该文件上叠着约 3300 行**与本任务无关的未提交大改**——为避免把两条工作流搅在一起，本轮不动（用户已确认），等大改落地后单独清一次。
5. ~~**补测试覆盖**~~ ✅ 已完成（两个并行 agent 分工补齐，只新建测试文件、不改共享代码）：
   - `internal/vm/macos_guest_identity_test.go`（4 个测试）：配置身份/固定 cageprovision 账号/时区、恢复镜像单图无 fallback、同名 Delete 后重建换新 UUID/MAC、拒绝 Linux 模板、Edit 保持身份 + exprs 纯函数不变量。
   - `internal/vm/macos_wrapper_test.go`（9 个测试）：`ErrLimaOverrides` 拒绝、重名拒绝、`_config/base.yaml` 不混入 + `NoContainerd`、CAGE_CONFIG 隔离（代理变量清空、不发明 guest-profile）、Edit 语义（应用 Settings、临时端口 ignore 规则、幂等不重写、拒绝不落盘、并发整写无 `.tmp` 残留）、孤儿 QEMU 精确识别 + Stop 只杀本 VM 的孤儿（hostagent 无 driver 不杀、他程序/他 VM 的 PID 不认）。
   - `internal/hostimage/linux_template_test.go`：`ClearLinuxTemplate` 正常释放、缺文件不报错、归属变化拒绝删除三态。
   - 分类结论：Clone、Mounts、`vmtest.Fake` 等场景随 Linux 代码删净，无需移植；`l.Start` 的完整行为（资源分配、mounts≠0 拒绝）没有不真 boot 的注入点，现状只能靠 e2e 覆盖——如需单测要给 `instance.StartWithPaths` 加 seam，本轮未加。
   - 验证：`internal/vm` 24、`internal/hostimage` 31，连同 `limaembed`、`guestos` 共 4 包 125 个测试通过，gofmt/vet 干净。
6. ~~**提交**~~ ✅ 已提交 `f7a9f1c`（提交前 `git reset` 统一取消暂存后重新圈定：191 个文件，+1331/−33756；排除项见"注意"）。

## 注意

- 工作区里 `docs/architecture.html` 的改动可能在本次会话开始前就已存在，提交前请确认归属。`go.sum` 现与基线完全一致（`go mod tidy` 已消化早前的漂移），无需处理。
- **`f7a9f1c` 未包含的四项（留在工作区，归属另一条工作流或属工作文档）**：`docs/architecture.html`（约 3300 行大改）、`docs/isolation-landscape.md` 与 `docs/isolation-layering.md`（2026-11-20 deep-research 调研文档）、`HANDOFF-lima-linux-removal.md`（本文档）。
- 已删除已被 gitignore 的构建产物 `internal/limaembed/assets/lima-guestagent.Linux-aarch64.gz`。
