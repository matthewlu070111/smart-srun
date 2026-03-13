# luci-app-jxnu-srun

OpenWrt 的 LuCI 插件，用于江西师范大学深澜校园网（SRun）自动认证与账号管理

感谢 [@guiguisocute](https://github.com/guiguisocute) 的协助！

## 预览
<p align="center">
    <img src="https://github.com/matthewlu070111/luci-app-jxnu-srun/raw/doc/img/README01.jpg">
</p>

## 依赖
- luci
- python3-light

## 功能

- 自动校园网认证，自动检测断线并尝试重连，支持tea网络，支持有线/无线接入
- 多校园网账号、热点接口管理，一键登录登出切换账号，一键切换校园网/热点
- 页面显示当前在线状态卡
- 可配置下线时段切到热点，恢复后切回校园网，以适应jxnu定时切断宿舍交换机电源的网络环境
- 运行日志落盘（`/var/log/jxnu_srun.log`）

### 未来功能
- 支持多号多拨负载均衡网络叠加
- 适配更多高校的深澜校园网环境
- 更多账号功能，如账号分组、规则管理
- ……

## 下载安装

1. 下载最新 ipk 包：
   - [Releases](https://github.com/matthewlu070111/luci-app-jxnu-srun/releases)，文件名为`luci-app-jxnu-srun_x.x.x_all.ipk
`
2. 安装 ipk：
   - `opkg install luci-app-jxnu-srun_x.x.x_all.ipk`
3. 启用服务：
   - `/etc/init.d/jxnu_srun enable`
   - `/etc/init.d/jxnu_srun restart`

## GitHub Actions 一键编译

仓库内置工作流：`.github/workflows/build-ipk.yml`

在 GitHub 页面进入：

- `Actions -> Build luci-app-jxnu-srun (SDK)`

点击 `Run workflow` 即可构建并下载 ipk 产物。
