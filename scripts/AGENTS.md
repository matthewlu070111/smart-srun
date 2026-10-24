# 开发主机工具

本目录仅在开发主机运行。按任务读取手册，不把部署流程带入普通文档修改。

| 任务 | 实现入口 | 手册 |
| --- | --- | --- |
| SDK 构建与验包 | [build_go_sdk.py](build_go_sdk.py)、[verify_go_sdk.py](verify_go_sdk.py)、[targets.json](../targets.json) | [构建与发布](../doc/development/build-release.md) |
| 清单与候选草稿 | [make_go_manifest.py](make_go_manifest.py)、[ci_go_release.py](ci_go_release.py) | [构建与发布](../doc/development/build-release.md)、[工作流约束](../.github/AGENTS.md) |
| 包部署与精确版本恢复 | [hot_update.py](hot_update.py)、[hot_update_go.py](hot_update_go.py) | [部署与恢复](../doc/development/deployment.md) |
| 文档与变更上下文 | [check_docs.py](check_docs.py)、[doc_context.py](doc_context.py) | [上下文维护](../doc/development/context.md) |

- `--dry-run` 只验证本地输入；`--probe` 只读设备；`--prepare` 会上传并核验，不能混称只读。
- 热部署需要真实 SDK 包和精确原版本恢复包；bundle/split 不混用，不退回逐文件覆盖。
- 官方 APK 用维护者密钥签名并原生可信验证；临时预览密钥产物不能晋升为正式身份。
- 不覆盖同版本产物，不补写猜测的历史记录；验收证据绑定包 SHA256。
- 缺少工具、验签或验收环境明确失败/未执行，不让门禁返回伪成功。
- 主机测试使用合成数据；私钥、校园账号、真实备份和设备日志不进入公开测试或报告。

参数先看脚本 `--help`。主机检查与依赖见 [tests/README.md](../tests/README.md)。
