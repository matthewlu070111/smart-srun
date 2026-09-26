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

> [!IMPORTANT]
> **当前默认下载版本是 [2.0.0rc1](https://github.com/matthewlu070111/smart-srun/releases/tag/2.0.0rc1)（Go 版，Latest）。** `main` 已采用 Go 实现，版本号仍保留 `rc1`，安装包与原候选版相同。
> **从 1.x 升级请先在 1.6.1 导出备份，再按设备架构手动安装；不要使用 1.x 内置一键更新跨版本升级。** 旧更新器会看到 Latest，但不支持 Go 安装包的架构选择。详见[备份与迁移指南](https://srun-doc.guiguisocute.com/guide/backup-migration)。
> Python 1.6.1 的[安装包](https://github.com/matthewlu070111/smart-srun/releases/tag/v1.6.1)和 [`1.6.1` 源码分支](https://github.com/matthewlu070111/smart-srun/tree/1.6.1)继续保留。

## 使用

1. 在[下载页](https://srun-doc.guiguisocute.com/guide/download)选择路由器型号，核对实际固件、包架构和包管理器：opkg 选择 `.ipk`，apk 选择 `.apk`。
2. 在 LuCI **系统 → 软件包** 更新列表并上传安装，完成后重新登录。apk依赖或签名问题见 [安装指南](https://srun-doc.guiguisocute.com/guide/install)。
3. 打开 **服务 → SMART SRun → 开始一键配置**，选择接入方式，确认认证地址与后缀，填写账号并保存。
4. 检查默认账号和 **启用** 状态，点击 **立即登录**。配置成功后，可通过向导提交学校预设。

2.0 使用 `/etc/smart-srun/config.json`，不自动读取或迁移 1.x 配置。首次升级可在 1.6.1 的进阶设置导出备份，再在 2.0 中预览并导入；导入后核对账号和网口，再启用自动守护。APK 可手动跳过签名验证安装，公钥为可选项；使用原生验签或内置更新器前需安装项目公钥，详见[安装与校验](https://srun-doc.guiguisocute.com/guide/download#安装与校验)。

切网可能中断游戏、语音等已建立的连接。即使校园网和热点都能上网，更换公网出口后也不能保证 FF14 等游戏不断线。smart-srun 不提供跨出口的会话迁移；切换耗时和联网探测反映网络恢复情况，不代表原连接得到保留。

详细说明请访问 [智慧深澜文档站](https://srun-doc.guiguisocute.com/)；

参与项目见 [贡献说明与致谢](https://srun-doc.guiguisocute.com/contribute/)。

![智慧深澜 LuCI 界面，包含校园网账号与热点配置](doc/img/smart-srun-overview.png)
