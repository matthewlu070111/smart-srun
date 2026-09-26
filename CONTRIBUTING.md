# 贡献指南

详细说明统一维护在 [智慧深澜文档站](https://srun-doc.guiguisocute.com/contribute/)。

- 功能修复与学校预设数据提交到本仓库；文档修改提交到 [smartsrun-doc](https://github.com/guiguisocute/smartsrun-doc)。
- [贡献学校预设](https://srun-doc.guiguisocute.com/contribute/presets)：使用插件一键配置的 Issue 草稿，记录实际参数与验证范围。
- [架构与认证策略扩展](https://srun-doc.guiguisocute.com/development/architecture)、[测试与发布](https://srun-doc.guiguisocute.com/development/validation)。
- 提交前核对本人 Git 作者身份；合并外部贡献保留原作者署名，不改写已发布历史。PR 说明具体问题、最终行为与验证结果。

## `go` 分支（smart-srun 2.0）

`go` 是 2.0 的开发分支，认证核心改用 Go，LuCI 界面保持不变。

- `core/**` 是设备端 Go 实现。旧 Python 运行时和专属测试保留在 Git 历史及 `main`，不再放入 `go` 工作树。
- `scripts/**` 和 `tests/**` 中的 Python 仅用于开发主机上的 SDK、发布、部署与 LuCI 契约检查；行为回归映射见 [tests/README.md](tests/README.md)。
- `root/usr/lib/lua/**` 与 `root/www/**` 两边共用：布局、字段、操作流程、中文文案与五步向导**冻结**，只改后端调用；前端仍是无依赖 ES5。
- 配置 v2 是有意的破坏性更新（`/etc/smart-srun/config.json`，强类型 JSON），不读 1.x 配置。
- 发行包内不得包含或调用 Python；Python 只作开发机工具。
- Go 门禁：`scripts/verify-go.sh`（gofmt + vet + 打乱顺序的单元测试 + 覆盖率 + race）。缺少工具链或 `core/` 时该脚本失败而不是跳过。
- 按完整用户流程实现，开发时运行编译、相关回归和危险副作用检查。PR 说明行为变化、执行过的验证与未执行的硬件/发布验收；不要把构建通过写成真机或独立审查通过。
- 开发主机完整检查：`bash scripts/verify-go.sh`、`python3 -m unittest discover -s tests -v`、`ruff check scripts tests`、`node --check root/www/luci-static/resources/smart_srun.js`。Linux 上安装 Lua 5.1、GNU make、C 编译器和 Node，避免遗漏契约检查。

### Go SDK 开发包

`targets.json` 锁定 SDK 下载哈希、feeds 提交和 Go 工具链。14 个 SDK 目标（12 种架构，其中 x86_64、aarch64_cortex-a53 各含两种包格式）已完成首构、ELF/ABI、安装载荷和版本命令检查；这些检查不等于完整核心、OpenWrt 安装或真机验收。`pending_architectures` 记录尚未完成首构检查的架构，具体产物的验收等级仍以发布清单为准。只有纯 LuCI 包使用 `all`/`noarch`，核心和 bundle 使用实际 SDK 架构。

在 Linux（Python 3.12+、OpenWrt SDK 主机依赖及现有 Go 引导工具链）运行：

```sh
python3 scripts/build_go_sdk.py --target x86_64-opkg-24.10.8 \
  --version 2.0.0rc1 --work-dir /tmp/smart-srun-sdk --bootstrap /usr/local/go
```

工作目录不能有空格。脚本保留构建日志，并输出包、`SHA256SUMS` 和包含源码文件哈希、实际包版本、架构、大小的 `build-record.json`。已记录的同版本产物不会覆盖；构建通过不等于安装或独立验收通过。

开发载荷上限为 **16 MiB**（D80，此前 10 MiB），唯一来源是 `targets.json` 的 `development_payload_limit_bytes`；设备端由 `update.MaxPayloadBytes` 强制。改动请只改这两处，不要在脚本里另写字面值。放宽的依据是 D20 的体积归因：体积主要来自 Go 标准库的 crypto、runtime、net/http 和 encoding/json，本项目自身的包约占 5%，写更小的代码省不出来。

MIPS 使用 `GOMIPS=softfloat`，并关闭内联以满足此前 10 MiB 的载荷上限；其他架构保留正常内联。这会增加部分 CPU 操作耗时，不能将模拟器微基准当作真机延迟或内存验收。16 MiB 之后是否恢复内联需要在完整 SDK 矩阵上实测体积与速度后再定，未随 D80 一并改动。比较用例在 `core/tests/performance/`，资源和网络表现仍需在目标设备测量。

`scripts/verify_go_sdk.py` 重新核对原生包元数据、完整载荷、ELF 架构/大小端、静态链接和 Go 构建参数。必须提供构建记录、开发机 Go 和新的报告目录；`--qemu` 或 `--execute` 才运行包内版本命令。APK 另需 `--apk` 和受信公钥目录 `--keys`。输出的 `validation.json` 按包 SHA256 记录 ELF 检查，`inspection.json` 保存具体结果；版本命令成功不等于核心功能、安装或真机通过。

```sh
python3 scripts/verify_go_sdk.py sdk/artifacts/2.0.0rc1/build-record.json \
  --go /usr/local/go/bin/go --qemu /usr/bin/qemu-aarch64 --output checks/arm64
```

APK 目标可通过 `--sign-key` 和 `--public-key` 传入维护者控制的密钥。私钥不得入库或上传到构建产物。脚本对自己的未签名输入执行离线签名，然后必须用公钥通过原生 `apk verify`，不能只相信签名命令的退出码。用户可明确选择 `apk add --allow-untrusted` 手动安装已确认来源的文件，公钥为可选安装项；指南必须说明它跳过签名验证。内置更新、恢复、热部署和发布验收仍需受信原生验签，不能把手动绕过验证记为签名验收通过。

### GitHub Actions 与发布

CI 在 push、PR 和手动运行时检查 Go 门禁、五种 Go 架构编译、LuCI/主机工具、工作流语法和秘密泄露。所有外部 Actions 固定提交，普通检查只有仓库读取权限，不接触签名私钥或校园账号。

- `build-go.yml`：输入 `2.0.0rcN` 或 `2.0.0`，执行完整锁定 SDK 矩阵、原生包校验、ELF/载荷与 QEMU 版本启动检查，生成 `release-candidate-<版本>` artifact。预览 APK 使用每次任务临时密钥，不能直接晋升为公开版本。
- `build-prerelease.yml`：输入完整 RC 版本，默认仅预览构建；启用 `publish_release` 后使用维护者密钥，生成待核对的 GitHub prerelease 草稿。
- `build-release.yml`：输入正式版本，构建并创建正式 release 草稿。发布阶段复用本次已校验产物，不再编译，不覆盖已有版本。

正式签名和草稿创建只允许上游 `matthewlu070111/smart-srun` 的 `go` 分支。维护者需要配置 `release-signing` environment：secret `SMARTSRUN_APK_SIGNING_KEY` 为 PEM 私钥，variable `SMARTSRUN_APK_PUBLIC_KEY` 为匹配的 PEM 公钥；建议限制分支并设置审核人。`release` environment 控制草稿创建，也应设置维护者审核。缺少正式密钥时任务失败，不自动生成发布身份，不降级为跳过验签。

每个候选附带真实架构包、同 SDK 分体 ZIP、`release-manifest.json`、覆盖所有文件的 `SHA256SUMS`、`build-records.tar.gz` 和正式公钥。PKG_VERSION 在源码中保留 `0.0.0`；SDK 副本分别使用 opkg `~rc` 和 APK `_rc`，二进制与页面显示统一的 `2.0.0rcN`。版本标签不加 `v`，与 Go 更新器的不可变下载地址一致。

维护者核对草稿中的变更、已知问题、签名指纹和实测范围，再发布。首个公开 RC 仍需完整候选验收；自动化不会把仅编译通过的架构标成核心/安装/真机/校园认证通过。学校账号只在私有环境中测试，禁止放入 CI secrets、日志或公开附件。

### Go 更新与开发部署

`srunnet update check` 异步检查官方发布清单，`update run 计划ID` 启动独立 procd Worker。`update status` 只读固定状态，即使主服务停止也可使用；等待命令被取消不会终止已经开始的原生安装。安装失败时先读取状态和实际包版本，再使用 `update recover` 恢复保留的原版本包；配置备份独立保存，不自动覆盖当前配置。APK 必须通过设备已有受信公钥验证。

发布清单由 `scripts/make_go_manifest.py` 从真实 SDK `build-record.json` 生成。验证报告按包 SHA256 关联，构建通过不会自动变成真机或校园验收通过。未提交源码的构建只能用 `--internal-test` 生成内部测试清单，不得公开发布。

部分衍生固件的版本号与包管理器不同步，例如 Kwrt 25.12 仍使用 opkg。额外固件系列必须先用相同 SHA256 的包完成原生安装验证，再在验证报告中添加 `"firmware_compatibility": {"包的 SHA256": {"25.12": {"openwrt_install": true}}}`。生成器保留实际 SDK 版本，并拒绝会让同一设备匹配多个包的兼容范围；这项证据不代表校园认证或长测通过。

```sh
python3 scripts/make_go_manifest.py sdk-a/artifacts/2.0.0rc1/build-record.json \
  sdk-b/artifacts/2.0.0rc1/build-record.json --evidence validation.json --output release/2.0.0rc1
```

输出目录必须不存在。脚本复制包并重新核对字节，生成清单和 `SHA256SUMS`；上传时使用整个输出目录。发布文件名增加实际包架构和 `_openwrt-<SDK版本>`，避免多架构 APK 原生同名，以及不同 SDK 的同名 IPK 相互覆盖。包内名称、版本和签名不变，构建目录继续保留原生文件名。清单最后写入，未完成的导出不会覆盖已有目录。

不同 SDK 目标独立签名的 `noarch` LuCI APK 可能只有签名字节不同。合并时另传 `--apk /path/to/apk --keys /path/to/trusted-keys`：两包都必须原生验签，且完整原生元数据（文件哈希、属性、依赖与脚本）一致。随后仅保留第一份包的原始字节和对应 SHA256 验证证据；不重新签名、不把另一份包的验收状态转移过来。内容不同或缺少验证工具时拒绝合并。

构建记录同时保存版本替换前的 `source_template_files` 和实际 SDK 输入的 `source_files`。跨包格式合并时比较原始源码，允许 Makefile 中 opkg `~rc` 与 APK `_rc` 的版本写法不同；其他文件必须一致。缺少原始源码测量的旧记录不能补写猜测值，应重新构建。

成功更新或恢复后，保留最近两次任务的恢复包与独立配置备份，删除已完成任务的临时下载。进行中的任务、损坏的 journal、没有完成记录的旧目录、未知文件与符号链接均不自动清理；清理失败会提示并在下一次更新前重试，不会把已经核验成功的安装说成失败。

本地上传包在复制、校验并同步到任务目录后会从上传区移除；任务成功完成后释放独立 worker 副本，避免持续占用 tmpfs。失败任务保留 worker 供恢复使用，持久恢复包与配置备份不受这项临时清理影响。

Go 树中的 `scripts/hot_update.py` 已转为 SDK 包部署入口，需要设备已安装支持 `update inventory` 的 Go 版本。首次安装使用原生包管理器。开发主机使用 Python 3.11+ 与 OpenSSH，设备无需 Python；SSH 认证、跳板与主机密钥沿用本机配置：

```sh
python3 scripts/hot_update.py --host router \
  --manifest new/release-manifest.json --assets new \
  --recovery-manifest old/release-manifest.json --recovery-assets old \
  --probe
```

`--dry-run` 只校验本地文件；`--probe` 读取设备安装信息并选择包，不上传或安装；`--prepare` 上传并完成设备端核验但不安装。去掉这些参数后执行更新，或加 `--background` 提交后返回。必须同时提供当前精确版本的恢复包，不能混用 bundle 与 split，也不能降级成逐文件覆盖。实际安装仍由独立 Worker 处理，主服务停止不会中断它。

### Versioned configuration backups

LuCI Advanced Settings and `srunnet config export FILE|-` produce a credential-bearing `smart-srun-config` envelope (`format_version: 1`). Explicit `config import FILE|- --check` previews 1.6.1 (`config_schema: 1`) or Go (`config_schema: 2`) exports; commit with `--expected-revision` from that preview. Imports replace configuration, remain subject to actor/update/wizard guards and CAS, and leave automatic authentication disabled. Startup still never reads the old Python config path. User-preset catalogues and system network state are separate. Never attach real backups to tests, Issues or logs.

The synthetic cross-version fixture is `tests/fixtures/config-backup-v1.json`; runtime, RPC, CLI and LuCI checks cover secret preservation, malformed input, stale previews and interrupted browser requests.
