# SDK 构建、验签与发布

本页随构建工具维护；入口见[贡献指南](../../CONTRIBUTING.md)。仅在构建、验包或发布任务中读取，命令从仓库根执行。

## SDK 开发包


[targets.json](../../targets.json) 锁定 SDK 下载哈希、feeds 提交、Go 工具链及目标矩阵；`pending_architectures` 记录待完成首构检查的架构。构建记录与对应包 SHA256 的发布清单记录实际验证范围，不在指南里维护一份“已全部通过”的架构列表。只有纯 LuCI 包使用 `all`/`noarch`，核心和 bundle 使用实际 SDK 架构。

在 Linux（Python 3.12+、OpenWrt SDK 主机依赖及现有 Go 引导工具链）运行：

```sh
: "${VERSION:?先设置本次尚未使用的候选版本，例如 2.0.0rcN 中的 N 替换为数字}"
python3 scripts/build_go_sdk.py --target x86_64-opkg-24.10.8 \
  --version "$VERSION" --work-dir /tmp/smart-srun-sdk --bootstrap /usr/local/go
```

工作目录不能有空格。脚本保留构建日志，并输出包、`SHA256SUMS` 和包含源码文件哈希、实际包版本、架构、大小的 `build-record.json`。已记录的同版本产物不会覆盖；构建通过不等于安装或独立验收通过。

开发安装载荷预算由 `targets.json` 的 `development_payload_limit_bytes` 定义；设备端限制见 [update.MaxPayloadBytes](../../core/internal/update/)。[make_go_manifest.py](../../scripts/make_go_manifest.py) 另有包文件字节上限，不能把压缩包大小与安装载荷视为同一个数。变更限制时核对构建、清单与设备校验，并以实际产物测量验证。

架构 ABI 与内联策略以 [Makefile](../../Makefile) 的 `GO_MIPS`、`GO_PKG_GCFLAGS` 为准，不在这里复制各架构编译选项。调整优化须在完整 SDK 矩阵测量体积与速度；[性能比较用例](../../core/tests/performance/)与模拟器微基准不等于目标设备的延迟、内存或网络验收。

[verify_go_sdk.py](../../scripts/verify_go_sdk.py) 重新核对原生包元数据、完整载荷、ELF 架构/大小端、静态链接和 Go 构建参数。必须提供构建记录、开发机 Go 和新的报告目录；`--qemu` 或 `--execute` 才运行包内版本命令。APK 另需 `--apk` 和受信公钥目录 `--keys`。输出的 `validation.json` 按包 SHA256 记录 ELF 检查，`inspection.json` 保存具体结果；版本命令成功不等于核心功能、安装或真机通过。

```sh
python3 scripts/verify_go_sdk.py \
  "/tmp/smart-srun-sdk/x86_64-opkg-24.10.8/artifacts/$VERSION/build-record.json" \
  --go /usr/local/go/bin/go --qemu /usr/bin/qemu-x86_64 --output "checks/$VERSION-x86_64"
```

APK 目标可通过 `--sign-key` 和 `--public-key` 传入维护者控制的密钥。私钥不得入库或上传到构建产物。脚本对自己的未签名输入执行离线签名，然后必须用公钥通过原生 `apk verify`，不能只相信签名命令的退出码。用户可明确选择 `apk add --allow-untrusted` 手动安装已确认来源的文件，公钥为可选安装项；指南必须说明它跳过签名验证。内置更新、恢复、热部署和发布验收仍需受信原生验签，不能把手动绕过验证记为签名验收通过。

## GitHub Actions 与发布

CI 在 push、PR 和手动运行时检查 Go 门禁、五种 Go 架构编译、LuCI/主机工具、工作流语法和秘密泄露。所有外部 Actions 固定提交，普通检查只有仓库读取权限，不接触签名私钥或校园账号。

- [build-go.yml](../../.github/workflows/build-go.yml)：输入候选或正式版本，执行锁定 SDK 矩阵、原生包校验、ELF/载荷与 QEMU 版本启动检查，生成候选 artifact。预览 APK 使用每次任务临时密钥，不能直接晋升为公开版本。
- [build-prerelease.yml](../../.github/workflows/build-prerelease.yml)：输入完整 RC 版本，默认仅预览构建；启用 `publish_release` 后使用维护者密钥，生成待核对的 GitHub prerelease 草稿。
- [build-release.yml](../../.github/workflows/build-release.yml) 与 [publish-go.yml](../../.github/workflows/publish-go.yml)：正式构建和草稿发布复用本次已校验产物，不再编译，不覆盖已有版本。

正式签名和草稿创建只允许上游 `matthewlu070111/smart-srun` 的 `main` 分支。维护者需要配置 `release-signing` environment：secret `SMARTSRUN_APK_SIGNING_KEY` 为 PEM 私钥，variable `SMARTSRUN_APK_PUBLIC_KEY` 为匹配的 PEM 公钥；建议限制分支并设置审核人。`release` environment 控制草稿创建，也应设置维护者审核。缺少正式密钥时任务失败，不自动生成发布身份，不降级为跳过验签。

候选附带真实架构包、同 SDK 分体 ZIP、`release-manifest.json`、覆盖产物的 `SHA256SUMS` 和构建记录；正式签名候选另带发布公钥。实际资产集合由 [ci_go_release.py](../../scripts/ci_go_release.py) 生成。PKG_VERSION 在源码保留 `0.0.0`；SDK 副本分别使用 opkg `~rc` 和 APK `_rc`，二进制与页面显示统一的 `2.0.0rcN`。版本标签不加 `v`，与 Go 更新器的不可变下载地址一致。

维护者核对草稿中的变更、已知问题、签名指纹和实测范围，按当前任务授权发布。候选仍需相应验收；自动化不会把仅编译通过的架构标成核心/安装/真机/校园认证通过。学校账号只在私有环境中测试，禁止放入 CI secrets、日志或公开附件。


## 发布清单与验证证据

发布清单由 `scripts/make_go_manifest.py` 从真实 SDK `build-record.json` 生成。验证报告按包 SHA256 关联，构建通过不会自动变成真机或校园验收通过。未提交源码的构建只能用 `--internal-test` 生成内部测试清单，不得公开发布。

部分衍生固件的版本号与包管理器不同步，例如 Kwrt 25.12 仍使用 opkg。额外固件系列必须先用相同 SHA256 的包完成原生安装验证，再在验证报告中添加 `"firmware_compatibility": {"包的 SHA256": {"25.12": {"openwrt_install": true}}}`。生成器保留实际 SDK 版本，并拒绝会让同一设备匹配多个包的兼容范围；这项证据不代表校园认证或长测通过。

```sh
# 将同一源码版本的实际构建记录路径传入，可继续列出其他 SDK 的记录
python3 scripts/make_go_manifest.py \
  "/tmp/smart-srun-sdk/x86_64-opkg-24.10.8/artifacts/$VERSION/build-record.json" \
  --evidence "checks/$VERSION-x86_64/validation.json" --output "release/$VERSION"
```

输出目录必须不存在。脚本复制包并重新核对字节，生成清单和 `SHA256SUMS`；上传时使用整个输出目录。发布文件名增加实际包架构和 `_openwrt-<SDK版本>`，避免多架构 APK 原生同名，以及不同 SDK 的同名 IPK 相互覆盖。包内名称、版本和签名不变，构建目录继续保留原生文件名。清单最后写入，未完成的导出不会覆盖已有目录。

不同 SDK 目标独立签名的 `noarch` LuCI APK 可能只有签名字节不同。合并时另传 `--apk /path/to/apk --keys /path/to/trusted-keys`：两包都必须原生验签，且完整原生元数据（文件哈希、属性、依赖与脚本）一致。随后仅保留第一份包的原始字节和对应 SHA256 验证证据；不重新签名、不把另一份包的验收状态转移过来。内容不同或缺少验证工具时拒绝合并。

构建记录同时保存版本替换前的 `source_template_files` 和实际 SDK 输入的 `source_files`。跨包格式合并时比较原始源码，允许 Makefile 中 opkg `~rc` 与 APK `_rc` 的版本写法不同；其他文件必须一致。缺少原始源码测量的旧记录不能补写猜测值，应重新构建。
