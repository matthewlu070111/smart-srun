# smart-srun：仓库工作指南

这是 OpenWrt 软件包仓库。当前树是 Go 2.x：设备核心在 `core/`，LuCI 与安装资源在 `root/`。
Python 1.x 请查 `1.6.1` 分支或 Git 历史，不要将其运行时规则应用到当前树。

## 按修改范围读取

只读取本次任务涉及的指南；从根目录启动的工具未必自动加载子目录文件。

| 修改范围 | 入口 |
| --- | --- |
| Go 核心、配置、认证、网络 | [core/AGENTS.md](core/AGENTS.md) |
| LuCI、向导、设备安装资源 | [root/AGENTS.md](root/AGENTS.md) |
| SDK、验包、部署工具 | [scripts/AGENTS.md](scripts/AGENTS.md) |
| CI、签名、发布草稿 | [.github/AGENTS.md](.github/AGENTS.md) |
| 测试、fixture、验收范围 | [tests/AGENTS.md](tests/AGENTS.md) |
| 学校预设、文档、图片 | [doc/AGENTS.md](doc/AGENTS.md) |

跨目录改动可先运行：

```sh
python3 scripts/doc_context.py --files core/internal/config/backup.go
python3 scripts/doc_context.py --base origin/main
```

工具按变更输出阅读材料与建议检查，不执行部署或发布。
[上下文地图](doc/development/context.md)说明资料归属与维护方式；
[CONTRIBUTING.md](CONTRIBUTING.md)是贡献入口，`CLAUDE.md` 只引用本文件。

## 全局边界

- 设备包不得包含或调用 Python、Node；安装内容以 [Makefile](Makefile) 为准。
- 保留既有文件布局与数据格式，功能修改不夹带批量格式化。
- 不提交校园凭据、真实配置备份、设备标识或签名私钥；内部计划与验收记录留在忽略的 `.codex/`。
- 保留贡献者署名和已发布历史；删除分支前核实内容已保留，保留 `main` 与 `downloads`。
- 按完整用户流程推进并运行相关检查；缺失环境、未执行的验收和失败结果如实记录。
- 构建、版本启动、原生安装、校园认证、长测是不同证据，不能相互代替。
- 发布与设备操作遵循当前任务授权；旧交接中的授权不是永久授权。

## 检查与按需流程

- 文档引用与触发规则：`python3 scripts/check_docs.py`。
- Go、主机和 LuCI 测试：[tests/README.md](tests/README.md)。
- 查询外部库、框架、SDK 或 CLI 用法时，按[Context7 查询约定](doc/development/context.md#查询外部技术文档)获取当前文档。
- SDK、签名与候选发布：[smart-srun-release](.agents/skills/smart-srun-release/SKILL.md)。
- 开发设备包部署与恢复：[smart-srun-deploy](.agents/skills/smart-srun-deploy/SKILL.md)。

新知识放到负责它的源码、模块指南或手册；根文件只留全局规则与入口。
