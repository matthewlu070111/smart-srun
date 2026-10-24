# 贡献指南

smart-srun 当前使用 Go 核心和无依赖 LuCI 界面。代码、学校目录及随源码维护的开发手册在本仓库；用户指南与文档站界面在 [smartsrun-doc](https://github.com/guiguisocute/smartsrun-doc)。

## 按任务开始

| 任务 | 入口 |
| --- | --- |
| 安装、配置或排障 | [用户指南](https://srun-doc.guiguisocute.com/guide/getting-started) |
| 提交学校预设 | [贡献流程](https://srun-doc.guiguisocute.com/contribute/presets)与[数据约定](doc/AGENTS.md) |
| 修改 Go 核心 | [核心地图与约束](core/AGENTS.md) |
| 修改 LuCI 或向导 | [LuCI 契约](root/AGENTS.md) |
| 运行检查、了解验收边界 | [测试入口与行为映射](tests/README.md) |
| 构建 SDK 包、签名或创建候选草稿 | [构建与发布手册](doc/development/build-release.md) |
| 部署、恢复或导入备份 | [部署手册](doc/development/deployment.md) |
| 修改文档或智能体上下文 | [资料归属与触发规则](doc/development/context.md) |

旧 Python 实现与专属测试保留在 `1.6.1` 分支及 Git 历史。设备包不包含 Python；`scripts/` 和 `tests/` 中的 Python 只在开发主机运行。配置 v2 不在启动时读取旧路径，显式跨版本备份导入见部署手册。

## 提交前

1. 读修改目录的 `AGENTS.md`；跨目录改动用下列工具选择相关资料。
2. 运行相关测试，命令及依赖见 [tests/README.md](tests/README.md)。
3. 核对 Git 作者身份，保留外部贡献者署名和已发布历史。
4. PR 写清问题、最终行为、已执行检查和未完成的硬件或发布验收。

```sh
python3 scripts/doc_context.py --base origin/main
python3 scripts/check_docs.py
```

这两个命令只提供上下文和离线文档检查。普通开发按完整用户流程推进；专项覆盖、独立审查和硬件验收的未完成项应保留，不得写成已经通过。
