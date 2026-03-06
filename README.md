# luci-app-jxnu-srun

OpenWrt 的 LuCI 插件，用于江西师范大学校园网（SRun）自动认证。

## 功能

- 自动校园网认证
- 页面显示当前在线状态
- 一键“立即登录”
- 按时段自动上下线（北京时间，时间可配置）
- 可选进入下线时段时强制下线
- 可选自动切换 SSID（下线时段/断网切到热点，恢复后切回校园网）
- 运行日志落盘（`/var/log/jxnu_srun.log`）

## 页面结构

LuCI 页面：`服务 -> 师大校园网`

- 基础设置：账号、运营商、密码、SSID 切换开关、校园网 SSID、热点 SSID
- 进阶设置：按时段自动上下线、下线/上线时间、连通性检测地址、检测间隔
- 日志：查看最近日志、清空日志

## 默认行为

- 默认检测间隔：`60` 秒
- 默认下线/上线时间：`00:00` / `06:00`
- 默认开启自动切换 SSID
- 进入下线时段时：先关闭校园网 SSID，约 10 秒后启用热点 SSID
- 回到上线时段时：先关闭热点 SSID，约 10 秒后启用校园网 SSID
- 非下线时段如果检测到校园网断连，会自动切到热点并持续探测恢复

## 安装

1. 安装 ipk：
   - `opkg install luci-app-jxnu-srun_*.ipk`
2. 启用服务：
   - `/etc/init.d/jxnu_srun enable`
   - `/etc/init.d/jxnu_srun restart`

## GitHub Actions 一键编译

仓库内置工作流：`.github/workflows/build-ipk.yml`

在 GitHub 页面进入：

- `Actions -> Build luci-app-jxnu-srun (SDK)`

点击 `Run workflow` 即可构建并下载 ipk 产物。
