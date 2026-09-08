# 更新说明

发布版本：`${VERSION}`

OpenWrt SDK：`${OPENWRT_VERSION}`

发布类型：正式版

## 新增

- 

## 修复

- 

## 改进

- 

## 安装与更新

下载 Assets 中的 `luci-app-smart-srun-bundle`，按设备实际包管理器选择 `.ipk`（opkg）或 `.apk`（apk）。包架构为 `all`，仍需兼容的 OpenWrt、Python 拆分依赖与 Lua CBI 环境。

```sh
# opkg 设备
opkg install ./luci-app-smart-srun-bundle_*.ipk
```

```sh
# apk 设备：来源已确认但没有受信任签名的本地包
apk add --allow-untrusted ./luci-app-smart-srun-bundle-*.apk
```

[分体包下载](${SPLIT_PACKAGES_URL}) · [安装指南](https://srun-doc.guiguisocute.com/guide/install) · [备份与更新](https://srun-doc.guiguisocute.com/reference/configuration)

bundle 与分体包互斥，切换包型前先备份并移除原包型。更新内容以本次发布说明为准。
