# AI Agent 隔离方案开源项目调研

> 调研日期：2026-11-20。方式：deep-research workflow（22 个来源、110 条候选结论、Top-25 对抗性核查）。
>
> ⚠️ **核查状态声明**：workflow 的 25 组核查 agent 因 API 限额（429）全部失败，下列结论为"来源提取 + 人工旁证"，未经过 workflow 的 3 票核查。其中对 Airlock 最关键的两个项目（anthropics/sandbox-runtime、microsoft/mxc）已独立搜索旁证，标 ✅；其余按可信度标注：◐ = 单源但与公开事实一致；? = 未复核，引用需谨慎。限额重置后可用 `resumeFromRunId: wf_c51ca56a-c43` 重放缓存、只重跑核查。

## 0. 一句话结论

Airlock 的分层设计（宿主侧网络判定、孔径统一语法、身份单向注入、档位可换策略不动）不是孤例，而是正在成形的业界共识：

- **Anthropic 官方 sandbox-runtime 与 Airlock 拟议的 Seatbelt 第三档几乎同构**（代理锚定出口 + Seatbelt + 宿主侧凭据保管）；
- **微软 mxc 把"单一策略 → 多档后端"做成了正式产品**，即 Airlock Tier 契约的工业级实现；
- **E2B 在生产规模上运行着与 Airlock 相同的三条不变式**（宿主侧出口判定、宿主侧凭据保管、写时复制隔离）。

## 1. 全景表

| 项目 | 隔离基底 | 网络出口 | 文件共享 | 凭据 | 对 Airlock 的价值 |
|---|---|---|---|---|---|
| anthropics/sandbox-runtime（原 claude-code-sandbox）✅ | macOS Seatbelt / Linux bubblewrap+seccomp / Windows WFP+专用账户 | 默认拒绝的本地代理 + 主机名白名单，可选 TLS 终止 | Seatbelt subpath 约束 + 自动拒绝 `.git/hooks` 等敏感路径 | 宿主侧保管、沙箱内只发限定凭据 | ★★★★★ 第三档设计的官方同款 |
| microsoft/mxc ✅ | 单一 JSON 策略 → 多档后端（Seatbelt/Bubblewrap/LXC/NVX microVM/Hyperlight/Windows Sandbox…） | 策略声明的 proxy/outbound | 策略声明 RO/RW/denied 路径 | — | ★★★★★ Tier 契约的直接验证 |
| E2B runtime ◐ | Firecracker microVM 单档，快照恢复启动 | 宿主侧 nftables SNI/Host 白名单 + 客机 CA bundle 由宿主 /init 控制 | 块级 COW rootfs（用户态 NBD） | opaque marker 宿主侧保管、出口处才解析 | ★★★★ 三平面的生产规模验证 |
| Docker Sandboxes ◐ | 自研跨平台 VMM（Hypervisor.framework/WHP/KVM） | 边界预定义 | 镜像 + 声明式挂载 | 运行时从 microVM 边界外注入 | ★★★★ 桌面跨平台 VMM 先例 |
| OpenHands ◐ | Docker 容器为主，docker/local/remote 三档可切换运行时 | 未内建 egress 策略 | bind mount + 可选 `:overlay` 写时复制 | — | ★★★ 多档接口一致的先例 + §8 选项 B 的先例 |
| SWE-ReX ◐ | 执行面抽象（Docker/Fargate/Modal/Daytona） | 无（文档明示不覆盖） | 无 | 仅控制面 API key（默认空串） | ★★★ 反例：只统一执行面、不统一策略面 |
| Daytona ? | 当前文档：容器命名空间 | 仅 ingress 反代，无 egress 文档 | S3 持久卷，无挂载安全约束 | — | ★★ 反例；注意其架构已从早期 Firecracker 方案转向 |
| Firecracker（基底）◐ | 极简 VMM + jailer，刻意只做块设备（弃 9p 文件直通） | — | — | — | ★★★★ 与 Airlock 孔径哲学的对照面 |
| gVisor（基底）◐ | 用户态内核 | — | — | — | ★★ 性能/攻击面权衡证据 |

## 2. 关键项目详述

### 2.1 anthropics/sandbox-runtime ✅ — Airlock 第三档的官方同款

npm 包 `@anthropic-ai/sandbox-runtime`（beta research preview），Anthropic 为 Claude Code 打造，可独立用于沙箱化 agent、MCP server、bash 命令与任意进程。

- **三平台**：macOS 动态生成 Seatbelt profile（`sandbox-exec`）；Linux bubblewrap + seccomp（需 socat/ripgrep）；Windows 专用本地账户 + WFP 出口围栏（按账户 SID 匹配）。
- **网络**：默认拒绝（deny-by-default）的本地代理 + 主机名白名单，配置项 `network.allowedDomains / deniedDomains / strictAllowlist`，可选 TLS 终止与凭据脱敏；白名单外即断网、无直连回退。**与 Airlock 的 netguard + Mihomo 同构，且同样是"代理锚定"而非"全捕获"。**
- **文件**：与 Airlock `internal/mounts` 的 BaseNames 思路一致——自动拒绝 `.git/hooks`、`.git/config`（除非显式 `allowGitConfig`）、`.mcp.json`、`.claude/commands`、`.claude/agents`、shell 启动文件。
- **凭据**：宿主侧保管。官方示例：代理在沙箱外持有 GitHub token，向沙箱内只发放限定范围的凭据——与 Airlock 的 Keychain 单向注入同构。
- **违规归因（值得抄的 UX）**：Seatbelt 日志行、seccomp 事件、代理拒绝统一记录到 attribution key，事后可用 `getViolationsForCommand(key)` 回查——Claude Code 自身的违规检测就用这套机制。
- **如实披露弱点**：README 明列 pseudo-tty、weakened isolation、`cap_setfcap` 在 Docker 默认集内会削弱隔离等逃逸路径。与 Airlock"软档必须标软"的立场一致。

### 2.2 microsoft/mxc ✅ — Tier 契约的工业级实现

"Policy-driven, layered isolation and containment"，微软为运行不可信代码（模型输出、插件、工具）打造的跨平台沙箱，Rust/.NET/Node SDK。

- **抽象意图 → 具体后端**：JSON 配置里写 `"process"` / `"vm"` / `"microvm"`，由 mxc 按操作系统解析到具体后端（`"process"` → ProcessContainer/Bubblewrap/Seatbelt；`"vm"` → Windows Sandbox；`"microvm"` → NVX）。另有 LXC、Hyperlight(+Unikraft)、IsolationSession、WSLc。**这正是 Airlock TierSpec"档位可换、策略不动"的语法设计参照。**
- **macOS Seatbelt 后端**：`sandbox_init()` 在 `fork()` 与 `exec()` 之间调用，无 root、无守护进程、免安装，要求 macOS 15+。已知坑：securityd/trustd/cfprefsd 等 Mach 服务需要锚定正则放行；macOS 缺少 Linux PID namespace 那样的无特权进程树跟踪——**这正是 Airlock 设计稿"最脆弱假设"里预警的两个坑，微软踩过并给出了处理方式。**
- **策略契约 v1.0.0**：文件（RO/RW/denied 路径）、网络（proxy、outbound）、UI（剪贴板/显示/GUI）三面。**注意：mxc 把 UI 孔径独立成面，比 Airlock 的三平面还多切了一刀**——剪贴板/屏幕共享在 Airlock 里目前归在孔径层，mxc 的切法值得在设计稿里对照评估。

### 2.3 E2B runtime ◐ — 三平面的生产规模验证

开源后端（[e2b-dev/runtime](https://github.com/e2b-dev/runtime/blob/main/docs/ARCHITECTURE.md)），据其架构文档（内部细节未独立复核）：

- **基底**：Firecracker microVM 单档，每沙箱一个 Firecracker 进程（独立 cgroup + netns）；亚秒级启动靠恢复预启动快照（内存 + 磁盘 + VM 状态，userfaultfd 懒加载内存页），非冷启动。
- **网络**：每沙箱一个 netns 槽位（veth + tap、宿主侧唯一 IP、NAT），宿主侧按槽位施 nftables 出口防火墙，检查 TCP 的 SNI/Host 头做域名允许/拒绝。**HTTPS 域名过滤靠宿主控制客机 TLS 信任库**：orchestrator 的 /init 请求把选定的 CA bundle 带进 VM，客机内 envd 原子安装并整体替换客机激活的 CA bundle——一种不经 MITM 的域过滤思路，与 Airlock 的 Mihomo TLS 拦截目标相同、机制不同。
- **文件**：块级写时复制——模板 rootfs 只读共享，每沙箱写落入 COW 缓存（用户态 NBD server 暴露给 Firecracker），暂停时导出脏块 diff。
- **凭据**：控制面只存 opaque marker 不存明文，orchestrator 在沙箱出口边界才解析成真值——宿主侧保管、单向注入，与 Airlock Keychain 模型直接可比。

### 2.4 Docker Sandboxes ◐ — 拒绝容器、自建跨平台 VMM 的理由

Docker 官方博客《Why microVMs? The architecture behind Docker Sandboxes》（未逐字复核，与公开发布一致）：

- 每个 AI-agent 会话一个专用 microVM（独立内核 + VM 内私有 Docker daemon），明确拒绝纯容器、WASM/V8 isolate、无沙箱三案。
- **拒绝容器的理由**：编程 agent 常需要自己构建和运行 Docker 容器，被迫 DinD（Docker-in-Docker）+ 提权，破坏隔离——**这是"agent 档位选容器"必须回答的问题，OpenHands 们用信任假设回答，Docker 认为不够。**
- **拒绝 Firecracker 的理由**：只面向 Linux/KVM 云环境，无原生 macOS/Windows 支持。于是自建跨平台 VMM，单代码库分别用 Hypervisor.framework / WHP / KVM——**这条路线正是 Airlock 既有做法（Apple VZ）的跨平台版。**
- 立场句（原文大意）："边界——agent 能看哪些文件、能连哪些端点、拿到哪些秘密——在 agent 运行前定义，而不是由 agent/LLM 决定。（An LLM deciding its own security boundaries is not a security model.）"

### 2.5 OpenHands ◐ — 多档运行时的接口一致先例

- 官方文档以 Docker 容器为 agent 运行时基底；代码库同时维护 docker / local / remote 三种可切换运行时（`openhands/runtime/impl/{docker,local,remote}`），动作执行协议跨档一致——"档位可换、接口不变"的多档实践。
- 文件共享：`SandboxConfig.volumes` 配置 bind mount（`/abs/host/path:/container/path[:mode]`）或 named volume，`:ro` 由用户配置；**bind mount 可加 `:overlay` 启用写时复制层**，容器内写入落到宿主侧 per-container upper/work 目录而不污染原路径——**没有统一的安全语法（.git 强制只读那种），也没有 egress 策略；但它给 Airlock 设计稿 §8 的选项 B（工作副本模式）提供了现成语法先例。**

### 2.6 SWE-ReX ◐ / Daytona ? — 两个有价值的反例

- **SWE-ReX**（SWE-agent 的 runtime SDK）：单一接口抽象多执行后端（本地 Docker、AWS Fargate、Modal、Daytona WIP），同一套 agent 代码不换后端。但搜索 agent 对其 README + 306 页文档的关键词扫描显示**零命中** network/egress/proxy/firewall、credential/secret/keychain、mount/read-only——即：**它统一了执行面，没有统一策略面；隔离强度完全继承自部署后端。这是 Airlock 决不能做成的样子：Tier 契约若只覆盖执行不覆盖策略，三平面就名存实亡。**
- **Daytona**：当前（©2026 版）架构文档描述的是容器级 Linux 命名空间隔离（独立进程/网络/挂载/IPC ns，runner 分配专用 vCPU/RAM/磁盘），**未提 Firecracker 或 macOS VM**（那应是 2025 年前后的早期架构，引用需以当前文档为准）。网络模型只有 ingress 反向代理（`{port}-{sandboxId}.{proxy-domain}`），架构页未描述任何 egress 白名单/代理链/断网策略；跨沙箱共享靠 S3 兼容对象存储的持久卷，文档未描述挂载安全约束。

### 2.7 基底证据：Firecracker / VEE'20 / gVisor ◐

- **Firecracker（NSDI'18 论文）**：保留 KVM、完全弃用 QEMU，自建极简 VMM（自 crosvm 裁剪、不到其一半代码量）；设备模型只留 virtio 网络/块设备 + 串口 + 部分 i8042（QEMU 支持 40+ 种设备）；jailer 在 VMM 进程外再包一层沙箱（chroot、pid/net ns、降权、seccomp 白名单仅 24 个 syscall）；每 VM 内存 <5MB、启动 <125ms、单机每秒可建 150 个。**刻意只做块设备、移除 9p 文件直通，理由是文件系统代码庞大复杂、只暴露块 IO 可保护宿主内核大片攻击面。**
- **VEE'20（Wisconsin）**：① 基底无全能赢家——Firecracker 网络延迟高，gVisor 内存管理与网络流更慢，不同 workload 适用不同基底（多档合理的学术旁证）；② 宿主内核联合代码覆盖率：原生 63,163 行 < Firecracker 77,392 行 < LXC 90,595 行 ≈ gVisor 91,161 行（总 806,318 行）——**gVisor 与容器执行的宿主内核代码量基本相同（频率不同），"把功能移出内核=缩小宿主攻击面"的直觉被数据质疑**；③ 容器套 VM 是有据可查的生产实践：AWS 把 serverless 函数跑在单租户 VM 内容器里，Azure 在单内核上用容器复用多租户——宿主内核 bug 在 Azure 可击穿租户隔离、在 AWS 不能。**这是"硬隔离层包软隔离层"分层的论据，也是 Airlock VM 档套在软档之上的原始出处。**
- 调研还抓取了 OpenAI–HuggingFace agent 入侵事件技术报告与 HF 时间线（见来源清单），作为"出口管控非可选"的现实事件证据（细节未核查，引用前需读原文）。

## 3. 对 Airlock 的可借鉴与不可借鉴

### 可借鉴（按优先级）

1. **TierSpec 语法**：参照 mxc 的抽象意图设计——策略里写 `"process"` / `"vm"` / `"microvm"`，由驱动按 OS 解析到具体后端，而不是让用户直接选后端实现。
2. **宿主沙箱档的验收基线**：sandbox-runtime 披露的弱点清单（pseudo-tty、weakened isolation、Mach 服务）直接拿来当 Airlock 阶段 2 的测试用例；mxc 的 securityd/trustd/cfprefsd 锚定处理方式是现成答案。
3. **违规归因 UX**：sandbox-runtime 把 Seatbelt 日志/seccomp 事件/代理拒绝统一到 attribution key 供回查——Airlock 的"链接外开决策 + 网络拒绝"目前缺这层可解释性。
4. **§8 选项 B 的先例**：OpenHands 的 `:overlay` 写时复制语法 + E2B 的块级 COW——工作副本模式不是 Airlock 发明的，语法可以抄。
5. **不经 MITM 的 HTTPS 域过滤**：E2B 用宿主控制客机 CA bundle 的思路，可作为 Mihomo TLS 拦截之外的第二机制评估（对 macOS 客机尤其相关）。
6. **UI 孔径独立成面**：mxc 把剪贴板/显示/GUI 从文件孔径里切出来单独声明——Airlock 的三平面图值得评估是否把 UI 孔径升为第四面。

### 不可照搬

1. **Firecracker 的"只做块设备"哲学**：Airlock 的产品前提（工作台 + 受控共享）需要文件孔径；教训不是砍掉孔径，而是保持孔径面最小 + 语法强制（BaseNames 只读就是做对了的那类事）。
2. **SWE-ReX 式"只统一执行面"**：Tier 契约必须覆盖策略面，否则三平面名存实亡。
3. **Daytona 式 ingress-only 网络**：与 Airlock 的 egress-first 立场相反；不要被"沙箱平台"的名号带偏。
4. **容器档的默认信任**：Docker 指出 agent 需要 DinD 时容器档会破功——若 Airlock 将来考虑 Linux 容器档（containerenv），必须先回答这个问题。

## 4. 对 docs/isolation-layering.md 的更新建议

- **"最脆弱假设"一节可以升级**：原假设"Seatbelt 能约束整棵进程树的网络到 gate 端口"——sandbox-runtime（Anthropic）与 mxc（Microsoft）的实践证明**可行且已在生产使用**，但两家都确认了 Mach 服务处理是必答题；Airlock 实现阶段 2 时应把 Mach 服务锚定列入工作量，而非当成意外。
- **第三档的定位从"备选"升级为"官方同款"**：Anthropic 把它做成了 Claude Code 的默认沙箱方向。是否提前阶段 2 的优先级，仍由"先完善现有功能"的决定说了算——但立项时不再需要论证"这条路有没有人走过"。

## 来源清单

- [anthropics/sandbox-runtime README](https://github.com/anthropics/sandbox-runtime/blob/main/README.md)（及 [镜像](https://github.masuit.org/anthropics/sandbox-runtime/blob/main/README.md)、[sandboxicon 技术页](https://sandboxicon.com/tech/sandbox-runtime/)、[Claude Code sandbox-environments 文档](https://github.com/Net-AI-Git/template-claude/blob/main/claude-code-docs/code.claude.com/docs/en/sandbox-environments.md)）— ✅ 已旁证
- [microsoft/mxc](https://github.com/microsoft/mxc)、[mxc schema 文档](https://github.com/microsoft/mxc/blob/main/docs/schema.md)、[Seatbelt 后端文档](https://github.com/microsoft/mxc/blob/main/docs/backends/seatbelt/seatbelt-backend.md)、[策略契约 v1.0.0](https://github.com/microsoft/mxc/blob/main/docs/containment-policy/1.0.0/policy.md)、[Microsoft Learn 平台支持](https://learn.microsoft.com/en-us/windows/apps/develop/mxc/platform-support) — ✅ 已旁证
- [E2B runtime 架构文档](https://github.com/e2b-dev/runtime/blob/main/docs/ARCHITECTURE.md) — ◐
- [Docker: Why microVMs?](https://www.docker.com/blog/why-microvms-the-architecture-behind-docker-sandboxes/)、[Docker Sandboxes 隔离文档](https://docs.docker.com/ai/sandboxes/security/isolation)、[local-vs-cloud](https://docs.docker.com/ai/sandboxes/cloud/local-vs-cloud/) — ◐
- [OpenHands runtime 文档](https://github.com/OpenHands/docs/blob/main/openhands/usage/architecture/runtime.mdx) — ◐
- [SWE-ReX](https://github.com/SWE-agent/SWE-ReX) — ◐
- [Daytona 架构文档](https://www.daytona.io/docs/en/architecture/) — ?
- [Firecracker 论文](https://css.csail.mit.edu/6.5660/2024/readings/firecracker.pdf)、[VEE'20 blending 论文](https://research.cs.wisc.edu/multifacet/papers/vee20_blending.pdf)、[gVisor 性能指南](https://github.com/google/gvisor/blob/master/g3doc/architecture_guide/performance.md) — ◐
- [OpenAI–HF 入侵事件技术报告](https://cdn.openai.com/pdf/67869394-cb91-4c12-888c-5cbd85c7814c/OpenAI-Hugging-Face%20Incident-Technical-Report.pdf)、[HF agent-intrusion 时间线](https://github.com/huggingface/blog/blob/main/agent-intrusion-technical-timeline.md) — 未读，仅收录
- [awesome-ai-coding-sandboxes](https://github.com/fhiltscher/awesome-ai-coding-sandboxes)、[apple/Containerization](https://github.com/apple/Containerization)、[microsoft/mxc 仓库](https://github.com/microsoft/mxc)、[navikt/cplt](https://github.com/navikt/cplt)、[Apache Magpie RFC-AI-0002](https://apache.googlesource.com/magpie/%2B/f63930ad5298671b4ebcdf9c46c8ef3485e04068/docs/rfcs/RFC-AI-0002.md)、[dbggr.dev: contain AI agent web access](https://dbggr.dev/articles/contain-ai-agent-web-access/) — workflow 抓取来源，未深读
