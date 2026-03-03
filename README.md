# luci-app-jxnu-srun

OpenWrt 的 LuCI 插件，用于江西师范大学校园网（SRun）自动认证。

## 编译（SDK / Buildroot）

1. 将本目录放到 OpenWrt 源码目录中，例如：
   `package/luci-app-jxnu-srun`
2. 选择插件：
   - `make menuconfig`
   - `LuCI -> Applications -> luci-app-jxnu-srun`
3. 编译：
   - `make package/luci-app-jxnu-srun/compile V=s`

## 安装

1. 将编译出的 `luci-app-jxnu-srun_*.ipk` 上传到路由器。
2. 安装：
   - `opkg install luci-app-jxnu-srun_*.ipk`
3. 打开 LuCI：
   - `服务 -> 师大校园网`
4. 填写配置：
   - 学工号
   - 运营商（`cmcc`/`ctcc`/`cucc`/`xn`）
   - 密码
5. 保存并应用，然后启用服务：
   - `/etc/init.d/jxnu_srun enable`
   - `/etc/init.d/jxnu_srun restart`

## 页面功能

- 顶部显示“当前在线状态”（调用 `client.py --status`）
- 提供“立即登录”按钮（调用 `client.py --once`）
- 支持夜间停用（北京时间 00:00-06:00）
- 支持夜间停用时可选强制下线（便于手机热点）

## GitHub Actions 一键编译

仓库已包含工作流：`.github/workflows/build-ipk.yml`

使用方式：

1. 推送到 GitHub 后，进入 `Actions -> Build luci-app-jxnu-srun`。
2. 点击 `Run workflow`，可选填写：
   - `target`（默认 `rockship`）
   - `subtarget`（默认 `armv8`）
3. 等待构建完成，在 Artifacts 下载 `luci-app-jxnu-srun*.ipk`。
