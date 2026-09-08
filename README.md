<p align="center">
  <a href="https://srun-doc.guiguisocute.com/"><img src="doc/img/logo.svg" width="96" height="96" alt="智慧深澜 Logo"></a>
</p>

<h1 align="center">智慧深澜 · smart-srun</h1>

<p align="center">OpenWrt 深澜校园网认证插件，支持有线、无线与多账号管理。</p>

<p align="center">
  <a href="https://srun-doc.guiguisocute.com/"><strong>访问文档站</strong></a> ·
  <a href="https://github.com/matthewlu070111/smart-srun/releases">下载安装</a> ·
  <a href="https://srun-doc.guiguisocute.com/guide/troubleshooting">故障排查</a>
</p>

## 使用

1. 从 [Releases](https://github.com/matthewlu070111/smart-srun/releases) 下载 `luci-app-smart-srun-bundle`：opkg 选择 `.ipk`，apk 选择 `.apk`。
2. 在 LuCI **系统 → 软件包** 更新列表并上传安装，完成后重新登录。依赖或签名问题见 [安装指南](https://srun-doc.guiguisocute.com/guide/install)。
3. 打开 **服务 → SMART SRun → 开始一键配置**，选择接入方式，确认认证地址与后缀，填写账号并保存。
4. 检查默认账号和 **启用** 状态，点击 **立即登录**。配置成功后，可通过向导提交学校预设。

账号、无线、多 WAN 配置与开发说明请访问 [智慧深澜文档站](https://srun-doc.guiguisocute.com/)；参与项目见 [贡献说明与致谢](https://srun-doc.guiguisocute.com/contribute/)。

![智慧深澜 LuCI 界面，包含校园网账号与热点配置](doc/img/smart-srun-overview.png)
