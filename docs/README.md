# Airlock 文档入口

首次使用 Airlock，从安装与代理配置开始；已有环境遇到问题时，直接查故障恢复。

> 说明：`docs/` 下的使用文档（`guide/`、[开始使用](getting-started.md)、[常见问题与恢复](troubleshooting.md)）沿用上游 Claude-Cage 的多平台内容，描述的是上游产品形态（Windows/Linux 宿主、`.exe`/`.deb` 安装包、Debian 客机），仅作参照。本仓库实际范围以根 [README](../README.md) 为准：macOS 宿主 · macOS 客机（内置 Lima / VZ）+ 本地 Linux MicroVM · Cloud SSH 与容器环境。

## 本仓库架构与设计文档

| 你要做什么 | 从这里开始 |
|---|---|
| 查看双档 VZ / 双栈网络 / 受限 SFTP 的源码级架构全景与优化矩阵 | [架构深潜（交互图）](architecture.html) |
| 了解隔离分层设计、Tier 契约与宿主沙箱档规划 | [隔离分层架构设计](isolation-layering.md) |
| 参阅业界 AI Agent 隔离方案调研（sandbox-runtime / mxc / E2B 等） | [隔离方案调研全景](isolation-landscape.md) |
| 了解客机机器身份的随机生成规则 | [机器身份](guest-identity.md) |
| 了解客机名称与初始设备配置 | [客机名称与设备](guest-profile.md) |
| 查看客机主机名的公开硬件名目录说明 | [主机名硬件目录](hostname-catalog.md) |

## 使用与故障排查（上游文档，仅供参照）

| 你要做什么 | 从这里开始 |
|---|---|
| 让自己的 Agent 下载、安装、配置 Cage，跟踪任务并排查故障 | [Agent 使用指南](guide/agent.md) |
| 从安装应用、配置代理到打开第一个项目 | [开始使用](getting-started.md) |
| 选择客机系统，了解原生工具、镜像与数据保留（上游多平台口径） | [选择客机系统](guide/guest-systems.md) |
| 查看快捷键、文件保存、关闭与退出行为 | [日常使用](getting-started.md#日常使用) |
| 升级应用、更新环境工具或卸载 | [升级与卸载](getting-started.md#升级与卸载) |
| 查看首个内测版本的能力与验收范围 | [0.1.0-alpha.1 发行说明](releases/0.1.0-alpha.1.md) |
| 排查下载、启动、联网与授权问题 | [常见问题与恢复](troubleshooting.md) |
| 导出故障日志并了解自动清理 | [导出诊断包](troubleshooting.md#导出诊断包) |
| 报告隔离或凭据安全问题 | [安全报告](../SECURITY.md) |

许可材料见 [LICENSE](../LICENSE)、[NOTICE](../NOTICE)和[第三方许可](../THIRD_PARTY_LICENSES)。
