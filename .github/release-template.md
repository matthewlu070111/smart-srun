# smart-srun ${VERSION}

Go 发布候选，源码 `${SOURCE_COMMIT}`。请维护者补充本次变更与验收结论后发布此草稿。

## 安装与更新

先在[下载页](https://srun-doc.guiguisocute.com/guide/download)选择路由器型号，并核对实际固件与包管理器。完整包包含 Go 核心和 LuCI；分体 ZIP 包含同版本核心和 LuCI，两种安装方式互斥。核心和完整包必须匹配设备架构，只有纯 LuCI 包与架构无关。

本次提供 ${ASSET_COUNT} 个独立包，具体架构、SDK、校验和与实测层级见 `release-manifest.json`。SDK 编译和版本启动检查不代表全部设备或校园环境已经验收。`SHA256SUMS` 同时覆盖安装包、分体 ZIP、公钥、清单和构建记录。

APK 可按[安装指南](https://srun-doc.guiguisocute.com/guide/download#安装与校验)使用 `apk add --allow-untrusted` 手动安装已确认来源的本地文件；此选项会跳过本次签名验证，无法验证发布者。`smart-srun-apk.pem` 公钥为可选安装项，公钥 SPKI SHA-256：`${APK_FINGERPRINT}`。需要原生验签或内置更新时，请先核对并安装公钥；内置更新器保持原生信任校验。IPK 文件校验和提供完整性核对，不等于软件源签名。

2.x 使用 `/etc/smart-srun/config.json` 和 `/etc/smart-srun/user-presets.json`，不自动读取或迁移旧配置。首次升级可在 1.6.1 导出备份，再按[升级指南](https://srun-doc.guiguisocute.com/guide/backup-migration)显式导入；导入后先核对账号和网口，再启用自动守护。设备运行不依赖 Python，也不会删除其他插件所需的系统 Python 包。

2.x 用户可通过 LuCI 或 `srunnet update` 获取同包型、架构及固件兼容的更新。资产一经公开不覆盖；修复使用新版本。
