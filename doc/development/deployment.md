# 更新、恢复与开发部署

本页随更新器和主机部署工具维护；入口见[贡献指南](../../CONTRIBUTING.md)。仅在设备更新、恢复或备份导入任务中读取，主机命令从仓库根执行。

## 更新器的行为边界


`srunnet update check` 异步检查官方发布清单，`update run 计划ID` 启动独立 procd Worker。`update status` 只读固定状态，即使主服务停止也可使用；等待命令被取消不会终止已经开始的原生安装。安装失败时先读取状态和实际包版本，再使用 `update recover` 恢复保留的原版本包；配置备份独立保存，不自动覆盖当前配置。APK 必须通过设备已有受信公钥验证。

成功更新或恢复后，保留最近两次任务的恢复包与独立配置备份，删除已完成任务的临时下载。进行中的任务、损坏的 journal、没有完成记录的旧目录、未知文件与符号链接均不自动清理；清理失败会提示并在下一次更新前重试，不会把已经核验成功的安装说成失败。

本地上传包在复制、校验并同步到任务目录后会从上传区移除；任务成功完成后释放独立 worker 副本，避免持续占用 tmpfs。失败任务保留 worker 供恢复使用，持久恢复包与配置备份不受这项临时清理影响。

## 开发设备部署

[hot_update.py](../../scripts/hot_update.py) 调用 [hot_update_go.py](../../scripts/hot_update_go.py)，需要设备已安装支持 `update inventory` 的 Go 版本。首次安装使用原生包管理器。开发主机使用 Python 3.11+ 与 OpenSSH，设备无需 Python；SSH 认证、跳板与主机密钥沿用本机配置：

```sh
python3 scripts/hot_update.py --host router \
  --manifest new/release-manifest.json --assets new \
  --recovery-manifest old/release-manifest.json --recovery-assets old \
  --probe
```

`--dry-run` 只校验本地文件；`--probe` 读取设备安装信息并选择包，不上传或安装；`--prepare` 上传并完成设备端核验但不安装。去掉这些参数后执行更新，或加 `--background` 提交后返回。必须同时提供当前精确版本的恢复包，不能混用 bundle 与 split，也不能降级成逐文件覆盖。实际安装仍由独立 Worker 处理，主服务停止不会中断它。

部署前核对当前任务授权的设备与实际安装版本。维护者既有设备约束是先更新 `.1` 并重新开始相应长测；`.2` 的活动校园凭据未知，保持只读。设备地址、凭据和运行记录放在私有交接中，不从历史任务推断本次获准修改设备。包部署成功也不代表校园认证或新一轮长测已经通过。

## 配置备份与显式导入

LuCI 进阶设置及 `srunnet config export FILE|-` 导出含凭据的 `smart-srun-config` 封装（`format_version: 1`）。`config import FILE|- --check` 显式预览 1.6.1（`config_schema: 1`）或 Go（`config_schema: 2`）备份，再使用该预览的 `--expected-revision` 提交。导入会替换配置，仍受 actor、更新和向导互斥及 CAS 保护，完成后自动认证保持关闭。启动仍不读取 Python 旧配置路径；自定义预设与系统网络状态独立管理。真实备份不能附在测试、Issue 或日志中。

合成跨版本样本是 [config-backup-v1.json](../../tests/fixtures/config-backup-v1.json)。运行时、RPC、CLI 与 LuCI 检查覆盖秘密保留、格式错误、过期预览与浏览器请求中断；实现入口是 [config/backup.go](../../core/internal/config/backup.go)，用户操作见[备份与迁移指南](https://srun-doc.guiguisocute.com/guide/backup-migration)。
