# 隔离分层架构设计

> 状态：设计稿，待拍板。承重约束：**Claude 网络隔离方案必须跨档位保留；所有策略判定点在宿主侧。**
>
> 决定（2026-11-19）：本设计整体后置——先把现有功能完善好，之后再按 §5 推进；宿主沙箱档在立项前保持"备选，不做"状态。

## 1. 现状核对（代码为准，非推测）

分层架构的一半**已经隐式存在于代码中**，本设计先把它显式化，再补缺的档位。

| 概念 | 代码对应物 | 状态 |
|---|---|---|
| 网络策略平面 | `internal/cage/egress.go`（`Egress`/`EgressGate`）+ `internal/netguard`（判定）+ `internal/managedproxy`（Mihomo 链） | 已存在。`egress.go` 注释写明关键不变式：单一上游、无直连回退、活在 hostagent、客机永远拿不到上游凭据 |
| 执行点（随档位变化） | Lima per-instance netstack（VM 系）、gvisor-tap-vsock 用户态栈（`internal/microvm/netstack.go`，直接绑定 `EgressGate`）、`internal/containerenv` | 已存在。`internal/vm/networkconfined.go` 已"失败即拒绝"：非 QEMU/VZ 驱动一律 `ErrNetworkOpen`，因为流量不经过受策略管的 netstack |
| 孔径层 | `internal/mounts`（统一共享语法 + `.git/.hg/.sl/.jj` 强制只读 safety 规则，MicroVM 的 SFTP 与 Lima 共用）、`clipboardfiles`、`exitpolicy` | 已存在 |
| 身份层 | `internal/maclogin` / `guestprofile` / `macnative`（Keychain） | 已存在。`internal/workbench/mobile_cli_identity_test.go:127` 已证明 seatbelt 能挡住 Claude CLI 读 Keychain 和个人目录 |
| 第三档（宿主沙箱） | 仅 3 个测试文件里的 `sandbox-exec` 内联 profile（`claude_handoff_cli_test.go` 等） | **缺失** |
| 档位驱动契约 | 无。macOS 客机档（Lima）和 MicroVM 档各自被 `cmd/cage` / `cage-desktop` 手工接线，没有统一的 TierSpec | **缺失** |

## 2. 分层模型

```
L4  操作面        mygoclient / workbench（UI，不参与安全模型）
─────────────────────────────────────────────────────────────
L3  策略平面      internal/cage（EgressGate）│ mounts │ maclogin/Keychain
                  （宿主独占，唯一决策点；客机/被沙箱进程里没有任何策略代码）
─────────────────────────────────────────────────────────────
L2  执行点        Lima netstack │ gvisor-tap-vsock │ seatbelt 内核锚定（新）
                  （档位各异，但都只做一件事：让每个字节必经 EgressGate）
─────────────────────────────────────────────────────────────
L1  基底          macOS VZ 客机 │ 本地 Linux MicroVM │ 宿主沙箱档（新）
```

三个安全平面（跨切关注点）：

1. **执行基底 + 跨界孔径**：VM 只是墙的材质（决定墙多硬）；真正决定隔离语义的是墙上的门——mounts、剪贴板、上传下载、链接外开（`exitpolicy`）。**孔径集合必须与档位无关**：换档位时孔径语义不变。
2. **网络出口**：所有 egress 必经宿主侧 EgressGate，netguard 判定 + Mihomo 链，域名白名单在 gate 处执行。
3. **身份/凭据**：token 只从宿主 Keychain 向会话单向注入，永不回流、不落共享目录。它在前两个平面都有投影，故必须独立成层。

**核心不变式**：三个平面的判定点全部在宿主侧，客机里永远只有执行、没有决策。这是整个架构最值钱的性质——档位可替换而策略不动。

## 3. Tier 契约（每个档位驱动必须满足的四条不变式）

1. **全捕获**：客机/被沙箱进程的每个出口字节必经宿主提供的 EgressGate——做不到就 `ErrNetworkOpen` 式拒绝启动（沿用现有先例）。
2. **同语法**：孔径全部消费同一份 mounts 规则和 safety 检查，不搞档位私有孔径。
3. **单向凭据**：见上，身份层第 3 条。
4. **档位可换、策略不动**：用户切档位时，`~/.agent-vm/network` 策略文件和 Mihomo 链不变。

验收即测试：每条不变式对应一组 e2e 用例（见 §6）。

## 4. 宿主沙箱档（新增的第三档）

**关键复用**：现有 `Egress` 本来就是一个带 netguard 判定、DoH、无回退上游的宿主侧代理。VM 档用 netstack 把流量"拉"过来；宿主档反过来，用 seatbelt 内核规则把进程"推"过去——

- 只放行 `connect()` 到 `127.0.0.1:<gate端口>`
- DNS 只许走 gate 的 DoH（拒绝直查 53）
- 文件系统部分：从 mounts 规则翻译成 seatbelt 的 subpath 读写规则，Keychain 与个人目录默认拒绝（测试已验证此路径可行）

两种档位**共用同一个 gate 实例和同一份判定代码**。网络隔离方案原样保留，只是执行点从用户态栈换成了内核规则。

**官方方案检查**：macOS 26 的 Swift Sandbox API 与 Apple Containerization framework 都要求 macOS 26+，本项目底线是 macOS 14，因此 `sandbox-exec` 是当前唯一可用的宿主沙箱原语。profile 生成收拢在单一包（`internal/hostsandbox`），将来 API 升级只换一个驱动。

**诚实的边界声明**：seatbelt 拦不住 URL-scheme handoff（被沙箱进程可让宿主浏览器带外访问）。因此宿主档在 UI 中标注为**软隔离档**：链接外开默认走 `exitpolicy` 的隔离浏览器（commit `4bd70b5` 已有先例），硬边界需求由 VM 档承担。可选强化：专用用户账户模式（阶段 4）。

## 5. 分阶段落地（每阶段独立可合并）

> **当前状态（2026-11-19）：按用户决定，各阶段全部后置，先完善现有功能；下表保留为将来立项时的推进顺序。**

| 阶段 | 内容 | 规模 | 单独合并后的状态 |
|---|---|---|---|
| 1 | 显式化 Tier 契约：TierSpec + 驱动接口，macOS 客机（Lima）和 MicroVM 改造成头两个驱动。纯重构，零行为变化 | 中（约 8–10 文件） | 系统不变，"三平面"从隐性变显性，新档位有章可循 |
| 2 | 新包 `internal/hostsandbox`：seatbelt profile 生成器 + Egress 钉定 + `cage sandbox run` CLI | 中大 | 第三档可用（CLI 级），轻量方案落地且网络隔离完整 |
| 3 | 工作台 UI 档位选择器（"启动环境"里选档），依赖阶段 2 | 中 | 产品化完成 |
| 4（可选，后置） | 宿主档强化：专用用户账户，关闭 URL-scheme handoff 缺口 | 小 | 不做也不影响前三阶段 |

**最小方案**：只做阶段 1。契约重构本身就是分层的落地。

**回滚成本**：阶段 1 纯移动可 revert；阶段 2、3 全部新增、默认关闭，不影响现有用户。

## 6. 验收测试路径（阶段 2 核心）

- **拒绝路径**：seatbelt 下 Claude CLI 读 Keychain 被拒（抄 `mobile_cli_identity_test` 先例）；直连非 gate 端口被拒；直查 DNS(53) 被拒。
- **放行路径**：经 gate 访问白名单域名成功；非白名单域名在 gate 处被 netguard 拒绝。
- **回归**：macOS 客机 / MicroVM 现有 e2e（`macos_guest_e2e_test.go`、`netstack_test.go`）在阶段 1 后原样通过。

## 7. 最脆弱的假设

**本设计假设 seatbelt 能约束整棵 Claude 进程树的网络到 gate 端口。** 若实测发现逃逸（launchd 代连、mach 服务、URL-scheme handoff），宿主档退化为"防误操作"级而非"硬边界"级。设计已为此变形：软隔离标注 + 链接默认走隔离浏览器 + 阶段 4 补洞选项。不会出现"假装是硬边界"的情况。

## 8. 待拍板

宿主沙箱档的文件系统语义（随宿主档一并后置，立项时再拍板），二选一：

- **A. 完全复用现有 mounts 共享语法**：宿主档 = 原目录直写、零拷贝。最轻，但隔离弱。
- **B. 写时复制/工作副本模式**：Claude 改的是副本，宿主目录只读，会话结束再决定是否合并回真目录。更符合"隔离工作台"直觉，需多一层 overlay。

选 A 则阶段 2 保持"中"规模；选 B 阶段 2 升为"中大"，且孔径不变式对宿主档要豁免一条（共享目录语义与 VM 档不同构）。
