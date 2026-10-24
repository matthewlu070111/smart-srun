# 文档与智能体上下文地图

先读[根指南](../../AGENTS.md)，再按修改范围进入模块。本文解释信息放在哪里；
安装和使用步骤在[文档站](https://srun-doc.guiguisocute.com/)，不复制到每份智能体指令。

## 一条事实只维护一处

| 信息 | 维护位置 | 其他入口怎么使用 |
| --- | --- | --- |
| 全局开发边界、目录导航 | [AGENTS.md](../../AGENTS.md) | [CLAUDE.md](../../CLAUDE.md) 只写 `@AGENTS.md` |
| 模块陷阱与约束 | 目录内 `AGENTS.md` | 根指南与变更工具指向它 |
| 配置、RPC、命令、状态的真实行为 | [core/internal](../../core/internal/) 及相邻测试 | 参考文章解释并链接符号，不抄完整 schema |
| 包内容、依赖和优化参数 | [Makefile](../../Makefile) | 构建说明引用安装宏与变量 |
| SDK、架构矩阵、工具链和载荷预算 | [targets.json](../../targets.json) | 不手写另一份目标清单 |
| 测试命令、依赖、行为映射 | [tests/README.md](../../tests/README.md) | 贡献页与模块指南链接过来 |
| SDK、验签、清单和候选发布 | [build-release.md](build-release.md) | 发布技能与文档站只负责引路 |
| 部署、恢复和备份导入 | [deployment.md](deployment.md) | 部署技能与文档站只负责引路 |
| 学校预设 | [school-presets.json](../school-presets.json) | 设备安装和镜像消费同一源文件 |
| 面向用户的教程、排障与历史版本说明 | [smartsrun-doc/docs](https://github.com/guiguisocute/smartsrun-doc/tree/main/docs) | 源仓 README 保留使用入口 |
| 私有任务计划、设备观察与验收证据 | 本地 `.codex/` | 按任务交接，不混入公共指南 |

`main` 已使用 Go。Python 1.x 的规则只适用于 `1.6.1` 分支及历史资料。
配置 v2 启动不读旧路径；显式备份导入是另一条用户流程，不能写成“完全不支持迁移”。

## 按变更选择上下文

从仓库根运行，工具仅用 Git 和 Python 标准库，不联网、不执行输出的命令：

```sh
# 开始任务：给出拟修改文件，即使还没有 diff
python3 scripts/doc_context.py --files core/internal/config/backup.go root/usr/lib/lua/luci/smart_srun/schema.lua
# 检查当前未提交变更（含暂存、未暂存、未忽略的新文件）
python3 scripts/doc_context.py
# 评审分支：merge-base 到 HEAD 的变更，加上当前未提交变更
python3 scripts/doc_context.py --base origin/main
```

[review-context.json](../../.github/review-context.json) 是触发规则的唯一来源，
[doc_context.py](../../scripts/doc_context.py) 读取它；不是依赖某个产品识别的特殊配置文件。
每条规则包含 `id`、`any` 或 `all` 路径模式，以及 `docs`、可选 `skills` 和 `checks`。
`any` 至少命中一个变更文件；`all` 中每个模式都要有文件命中；同时存在时两者都须满足。
模式使用 Python `fnmatchcase`，其中 `*` 可以跨目录分隔符。

例如配置与 LuCI schema 同时改变时，工具同时提示核心、界面和配置契约；
只改一段普通文档不会要求读取设备部署流程。重命名前后路径与删除路径都参与匹配。
技能只在相应操作任务中使用，建议检查仍应按改动风险选择。

[CI](../../.github/workflows/ci.yml) 实际调用文档检查与该工具，把当前 diff 的阅读材料写入日志。
它不会自动注入所有智能体的上下文，也不会将输出的验收或部署命令当成已经执行。
从根目录启动会话时，应主动读取本次变更命中的目录指南。
Codex 的目录发现行为见[官方说明](https://learn.chatgpt.com/docs/agent-configuration/agents-md)。

## 长流程按需读取

- [smart-srun-release](../../.agents/skills/smart-srun-release/SKILL.md)：SDK 构建、候选验包、签名和发布准备。
- [smart-srun-deploy](../../.agents/skills/smart-srun-deploy/SKILL.md)：开发设备部署与恢复。

技能的描述只说明何时使用，正文链接到上面的手册。普通代码修改、README 校字不需要加载这两个流程。
不要为了列齐目录而增加没有具体用途的技能；学校预设的简短规则留在 [doc/AGENTS.md](../AGENTS.md) 即可。

## 防止资料过期

```sh
python3 scripts/check_docs.py
python3 -m unittest discover -s tests -p test_documentation_tools.py -v
```

[check_docs.py](../../scripts/check_docs.py) 离线检查受版本控制及未忽略的新 Markdown：
相对文件链接、`CLAUDE.md` 引用，以及触发配置中的文档、技能和检查目标是否存在。
代码示例中的伪链接不参与检查。它不验证外部网址、页内锚点或文字是否符合源码；这些仍需针对性核对。
文档站另用构建检查页面死链，维护方法见[本站说明](https://srun-doc.guiguisocute.com/development/documentation)。

改文档时引用符号名，不引用易漂移的行号；描述某个历史结果时附版本或固定提交链接。
修改接口时同步对应参考文章；移动文章保留旧 URL 或兼容入口。
使用与开发手册、文档站配套的新入口应随两仓改动一起交付，避免文档站先指向尚未合入的文件。

## 查询外部技术文档

需要库、框架、SDK、API 或 CLI 的现行用法时，用 Context7；纯重构、业务逻辑调试和代码评审不强制查询。
先执行 `npx ctx7@latest library <名称> "具体问题"` 获得 ID，再执行
`npx ctx7@latest docs /org/project "单一概念"`。选官方匹配、高信誉来源；每个问题不超过三次命令。
查询不包含密钥或凭据。额度不足应告知并提示 `npx ctx7@latest login` 或 `CONTEXT7_API_KEY`，不悄悄退回旧知识。
请求按会话权限在可联网环境执行；网络失败时说明环境限制，不反复在同一受限环境重试。
