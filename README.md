# luci-app-smart-srun

智慧深澜，OpenWrt 深澜校园网Web自动认证客户端，提供 CLI / LuCI 两种使用方式。

## 鸣谢
- [@guiguisocute](https://github.com/guiguisocute) 的协助

- [LINUX DO](https://linux.do/)
## 预览

### LuCI Web 界面
<p align="center">
    <img src="doc\img\PixPin_2026-04-24_18-32-26.png">
</p>

## 功能

- 自动校园网认证，自动检测断线并重连，支持有线 / 无线接入
- 多校园网账号、多热点配置管理，一键登录登出切换
- 可配置夜间时段自动切换到热点，恢复后自动切回校园网（适应宿舍定时断网环境）
- 结构化运行日志落盘（`/var/log/smart_srun.log`），支持分层日志等级（ALL / DEBUG / INFO / WARN / ERROR），LuCI 日志面板按等级着色
- 完整 CLI支持（`srunnet`）：状态查询、登录登出、配置管理、账号 / 热点 CRUD，功能对齐 LuCI
- 支持多学校配置文件，可扩展适配其他深澜校园网环境

### 未来功能
- 适配 UA3F应对多设备检查的环境
- 支持多号多拨负载均衡网络叠加
- 适配更多高校的深澜校园网环境(进行中)
- 更多账号功能，如账号分组、规则管理
- ……

## 安装包说明

仓库构建产出三个 ipk 包：

| 包名 | 说明 | 依赖 |
|------|------|------|
| `smart-srun` | 基础包：守护进程 + CLI | `python3-light` |
| `luci-app-smart-srun` | 标准 LuCI Web 界面包（用于 opkg / LuCI 软件包管理升级） | `smart-srun`、LuCI 运行环境 |
| `luci-app-smart-srun-bundle` | 自包含安装包：CLI + LuCI 一起打包，适合手动下载安装 | `python3-light`、LuCI 运行环境 |

- 最少安装步骤：直接安装 `luci-app-smart-srun-bundle`
- 仅CLI：安装 `smart-srun` 即可
- 需要Luci界面且需要依赖分离：安装 `smart-srun` + `luci-app-smart-srun`

## 安装
**以下操作请在路由器连上互联网的情况进行！**

1. 下载最新安装包：[Releases](https://github.com/matthewlu070111/smart-srun/releases)
   - OpenWrt 23.05 及更早（opkg 系统）→ 下载 `*.ipk`
   - OpenWrt 24.10+ / 25.12+（apk 系统）→ 下载 `*.apk`
2. 安装：
#### 使用 LuCI 网页面板安装：
1. 登录 LuCI 界面，进入 **系统**——**软件包** 页面。
2. 点击 **更新列表** 按钮，等待`opkg update` / `apk update`完成。
3. 点击 **上传软件包...** 按钮，选择自己下载的安装包。
- 如果需要LuCI Web界面，请**先**安装 `smart-srun`， **再**安装 `luci-app-smart-srun`。
- 如果你想直接安装一个包完成部署，直接上传 `luci-app-smart-srun-bundle`。（推荐）
4. 点击 **安装** 按钮，等待安装完成。
5. 出现安装成功的弹窗后，退出 LuCI 界面，重新登录，使新软件包生效。

> [!WARNING]
> **如果你使用的包管理器为apk**
> 
> 因为OpenWrt 25.12+的LuCI 的「上传软件包」按钮目前对**未签名的第三方 apk 包拒绝安装**（[openwrt/luci#8482](https://github.com/openwrt/luci/issues/8482)）。
>
> 所以如果上传失败出现 `UNTRUSTED signature`，请SSH进你的设备，改用下面的命令行方式，加 `--allow-untrusted` 参数。
---
#### 使用命令行界面安装：
1. 将安装包上传到 OpenWrt 设备，切换到该目录，执行：

**opkg 系统（OpenWrt 23.05 及更早）：**
```sh
# 仅 CLI
opkg install smart-srun_*.ipk

# 标准 split 安装
opkg install smart-srun_*.ipk
opkg install luci-app-smart-srun_*.ipk

# bundle 单包安装
opkg install luci-app-smart-srun-bundle_*.ipk
```

**apk 系统（OpenWrt 24.10+ / 25.12+）：**
> 因为本项目是自签的第三方包，apk 默认会因 `UNTRUSTED signature` 拒绝安装，
> 所以下面命令统一加 `--allow-untrusted`。这是 apk 的强制安全检查，
```sh
# 仅 CLI
apk add --allow-untrusted ./smart-srun-*.apk

# 标准 split 安装
apk add --allow-untrusted ./smart-srun-*.apk
apk add --allow-untrusted ./luci-app-smart-srun-*.apk

# bundle 单包安装
apk add --allow-untrusted ./luci-app-smart-srun-bundle-*.apk
```
2. 启用服务：
```sh
/etc/init.d/smart_srun enable
/etc/init.d/smart_srun restart
```

安装建议：
- **不要把 `luci-app-smart-srun-bundle` 和标准 split 包混装**

## 使用
### LuCI 使用

在 LuCI 页面进入 **服务 → SMART SRun**，在「基础设置」标签页中：

- **登录配置**：选择学校
- **校园网账号**：添加学工号、密码、运营商，支持多账号管理；有线账号可分别绑定 `wan`、`wan.v2` 等接口
- **多 WAN 并行认证**：打开全局开关，并在每个有线账号中启用“参与并行守护”，后台会同时维护所有已勾选线路
- **热点配置**：配置个人热点 SSID 和密码，供夜间自动切换使用
- **手动登录 / 登出**：随时触发，带进度反馈弹窗
- **手动切网**：一键切到热点或切回校园网

保存并应用后守护进程自动启动。

## 获取学校预设与环境真实字段值

### 学校预设
插件优先通过 [srun.guiguisocute.com](https://srun.guiguisocute.com/school-presets.json) 获取学校预设。

备用来源依次为官方 Pages 域名 `smart-srun--cloudflare-pages.pages.dev`、GitHub 原始文件、旧域名 `srun.edu-publish.site`；网络来源不可用时使用本地缓存和随包清单。

学校预设只填入已知参数，账号仍需自行填写。

已补录 Issue #28 的北京中医药大学参数；投稿未提供接入方式与 SSID，应用后请核对。Issue #29 的浙江工业大学采集信息保留为草稿，待插件认证复测通过后再加入默认选项。

### 环境真实字段值
**👉 [smart_srun_school_preset_capture.user](https://github.com/guiguisocute/smart_srun_school_preset_capture.user)**（含一键安装链接和详细说明）

简要使用：装好 Tampermonkey（油猴）后，从上面仓库一键安装，打开学校的深澜认证页，按照油猴脚本的GUI提示操作，即可采集到当前认证环境的运营商后缀与其他关键信息


```sh
srunnet detect acid
srunnet detect acid 'http://<portal-host>/srun_portal_pc?ac_id=<id>'
```

### CLI 使用

安装后可直接使用 `srunnet` 命令（无参数等同 `srunnet status`）：

```sh
# 查看当前状态
srunnet
srunnet status

# 查看帮助
srunnet help
srunnet help config
srunnet man

# 查看当前安装包类型与版本
srunnet --version

# 登录 / 登出 / 重新登录
srunnet login
srunnet logout
srunnet relogin

# 如果只想保持登出，请先禁用守护服务
srunnet disable
srunnet logout

# 查看实时日志（Ctrl+C 退出）
srunnet log

# 查看最近 50 行日志
srunnet log -n 50

# 查看当前学校 runtime 诊断信息
srunnet log runtime

# 启用 / 禁用守护服务
srunnet enable
srunnet disable

# 手动切换网络
srunnet switch hotspot
srunnet switch campus

# 列出可用学校配置（JSON 输出）
srunnet schools

# 查看当前生效学校 runtime 的详细信息（JSON 输出）
srunnet schools inspect --selected
```

#### 配置管理

```sh
# 查看完整配置
srunnet config
srunnet config show

# 查询 / 设置单个标量值
srunnet config get interval
srunnet config set interval=30 enabled=1
srunnet config set log_level=DEBUG

# 从 JSON 文件导入配置
srunnet config set -f my_config.json

# 校园网账号管理
srunnet config account              # 列出所有账号
srunnet config account add          # 交互式添加账号
srunnet config account edit campus-1
srunnet config account rm campus-2
srunnet config account default campus-1

# 热点配置管理
srunnet config hotspot              # 列出所有热点
srunnet config hotspot add          # 交互式添加热点
srunnet config hotspot edit hotspot-1
srunnet config hotspot rm hotspot-2
srunnet config hotspot default hotspot-1
```

有线多 WAN 场景下，每个校园网账号还可设置独立的 `wired_iface`。例如校园网
DHCP 地址位于 `wan.v2`，就在 LuCI 的账号编辑框中选择“有线（指定接口）”并填写
`wan.v2`；登录时会动态读取该接口的 IPv4，并同时绑定源地址和所属 L3 设备，
避免策略路由把认证请求送到其它 PPPoE WAN。不应把某次 DHCP 获得的地址写死到配置中。

如需三条 WAN 同时进行 Web 认证：先设置 `multi_wan_enabled=1`，再为三个有线账号
分别填写接口、学工号、密码和运营商后缀，并把每个账号的 `auth_enabled` 设为 `1`
（LuCI 中即“参与并行守护”）。每条线路独立查询、登录和退避重试；状态会按账号显示在
校园网账号表中。关闭全局开关后保持旧版行为，只维护当前默认账号。

托管线路缺少地址、设备或无法完成接口绑定时，该线路会报告失败，避免认证请求
经其他 WAN 发出。静默时段会暂停守护；若开启强制下线，也会处理当前活动的有线
账号，即使它没有勾选“参与并行守护”。

手动登录失败时，进度弹窗保留这次操作的具体结果，并可打开当前配置的学校认证页。
“认证网关可达”只说明网关能访问；请结合错误提示核对认证地址、AC_ID 与账号参数。

****
## License
[WTFPL](LICENSE)
## 参与贡献
如果你愿意参与开发：请参阅 [贡献指南](CONTRIBUTING.md)
