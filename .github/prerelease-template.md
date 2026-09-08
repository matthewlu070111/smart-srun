# What's Changed

发布版本：`${VERSION}`
OpenWrt SDK 版本：`${OPENWRT_VERSION}`

**⚠此版本为测试版！**

## New
- 

## Fixes
- 

## Improvements
- 

---

说明：
- 本软件包为纯脚本（Lua/Python/Shell），与 CPU 架构无关，所有 OpenWrt 设备均可直接安装。
- 请直接下载本页 Assets 中的 `luci-app-smart-srun-bundle_*.ipk`（opkg / 官方 OpenWrt 24.10 及更早）或 `luci-app-smart-srun-bundle-*.apk`（apk / 官方 OpenWrt 25.12 及更新）进行安装；第三方固件以实际包管理器为准。
- 安装命令：
  - opkg：`opkg install ./luci-app-smart-srun-bundle_*.ipk`
  - apk：`apk add --allow-untrusted ./luci-app-smart-srun-bundle-*.apk`
    > 本项目是自签第三方包，apk 默认会因 `UNTRUSTED signature` 拒绝安装，必须加 `--allow-untrusted`。这是 apk 的强制安全检查，不是包本身的问题。
- 如果你希望下载 CLI 与 LuCI 分离的 ipk/apk 包，请点击[这里](${SPLIT_PACKAGES_URL})。
