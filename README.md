# luci-app-smart-srun

智慧深澜，OpenWrt 深澜校园网（SRun 4000）自动认证客户端，提供 CLI / LuCI 两种使用方式。

感谢 [@guiguisocute](https://github.com/guiguisocute) 的协助！

## 预览

### LuCI Web 界面
<p align="center">
    <img src="https://r2.guiguisocute.cloud/PicGo/2026/03/25/aaa7b014a8a2db7fcc05edb9f81c242b.png">
</p>

## 功能

- 自动校园网认证，自动检测断线并重连，支持有线 / 无线接入
- 多校园网账号、多热点配置管理，一键登录登出切换
- 可配置夜间时段自动切换到热点，恢复后自动切回校园网（适应宿舍定时断网环境）
- 结构化运行日志落盘（`/var/log/smart_srun.log`）
- 完整 CLI支持（`srunnet`）：状态查询、登录登出、配置管理、账号 / 热点 CRUD，功能对齐 LuCI
- 支持多学校配置文件，可扩展适配其他深澜校园网环境

### 未来功能
- 加入TLS支持以应对HTTPS认证页面的环境
- 适配 UA3F应对多设备检查的环境
- 支持多号多拨负载均衡网络叠加
- 适配更多高校的深澜校园网环境
- 更多账号功能，如账号分组、规则管理
- ……

## 安装包说明

仓库构建产出三个 ipk 包：

| 包名 | 说明 | 依赖 |
|------|------|------|
| `smart-srun` | 基础包：守护进程 + CLI | `python3-light` |
| `luci-app-smart-srun` | 标准 LuCI Web 界面包（用于 opkg / LuCI 软件包管理升级） | `smart-srun`、LuCI 运行环境 |
| `luci-app-smart-srun-bundle` | 自包含安装包：CLI + LuCI 一起打包，适合手动下载安装 | `python3-light`、LuCI 运行环境 |

- 只需要 CLI：安装 `smart-srun` 即可
- 通过 LuCI / opkg 正常升级：安装 `smart-srun` + `luci-app-smart-srun`
- 手动下载安装、想少折腾：直接安装 `luci-app-smart-srun-bundle`

## 安装与使用

### 安装
**以下操作请在路由器连上互联网的情况进行！**

1. 下载最新 ipk 包：[Releases](https://github.com/matthewlu070111/luci-app-smart-srun/releases)
2. 安装：
   #### 使用 LuCI 网页面板安装：
   1. 登录 LuCI 界面，进入 **系统**——**软件包** 页面。
   2. 点击 **更新列表** 按钮，等待`opkg update`完成。
   3. 点击 **上传软件包...** 按钮，选择自己下载的 ipk 包。
    - 如果需要LuCI Web界面，请**先**安装 `smart-srun`的ipk， **再**安装 `luci-app-smart-srun` 的ipk。
    - 如果你想直接安装一个包完成部署，直接上传 `luci-app-smart-srun-bundle` 的ipk。（推荐）
   4. 点击 **安装** 按钮，等待安装完成。
   5. 出现安装成功的弹窗后，退出 LuCI 界面，重新登录，使新软件包生效。

   #### 使用命令行界面安装：
   6. 将ipk文件上传到OpenWrt设备，并切换到设备目录，执行：
   ```sh
    # 仅 CLI
    opkg install smart-srun_*.ipk
    ```
    ```sh
    # 标准 split 安装
    opkg install smart-srun_*.ipk
    opkg install luci-app-smart-srun_*.ipk
    ```
    ```sh
    # bundle版本单ipk文件安装
    opkg install luci-app-smart-srun-bundle_*.ipk
    ```
   7. 启用服务：
   ```sh
    /etc/init.d/smart_srun enable
    /etc/init.d/smart_srun restart
    ```

安装建议：
- **不要把 `luci-app-smart-srun-bundle` 和标准 split 包混装**

### LuCI 使用

在 LuCI 页面进入 **服务 → SMART SRun**，在「基础设置」标签页中：

- **登录配置**：选择学校
- **校园网账号**：添加学工号、密码、运营商，支持多账号管理
- **热点配置**：配置个人热点 SSID 和密码，供夜间自动切换使用
- **手动登录 / 登出**：随时触发，带进度反馈弹窗
- **手动切网**：一键切到热点或切回校园网

保存并应用后守护进程自动启动。

### CLI 使用

安装后可直接使用 `srunnet` 命令（无参数等同 `srunnet status`）：

```sh
# 查看当前状态
srunnet
srunnet status

# 查看当前安装包类型与版本
srunnet --version

# 登录 / 登出 / 重新登录
srunnet login
srunnet logout
srunnet relogin

# 查看实时日志（Ctrl+C 退出）
srunnet log

# 查看最近 50 行日志
srunnet log -n 50

# 启用 / 禁用守护服务
srunnet enable
srunnet disable

# 手动切换网络
srunnet switch hotspot
srunnet switch campus

# 列出可用学校配置（JSON 输出）
srunnet schools
```

#### 配置管理

```sh
# 查看完整配置
srunnet config
srunnet config show

# 查询 / 设置单个标量值
srunnet config get interval
srunnet config set interval=30 enabled=1

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
****
## 开发者指南
### 项目结构

```
root/
├── etc/init.d/smart_srun          # procd 服务管理脚本
├── scripts/
│   └── hot_update.py              # 开发态热更新脚本（上传到路由器并做远端校验）
├── tests/
│   ├── test_school_runtime_cli.py # CLI / LuCI 源码与开发脚本回归测试
│   └── ...                        # 其他运行时、配置与调度测试
├── usr/bin/srunnet                # CLI 入口脚本
├── usr/lib/smart_srun/
│   ├── client.py                  # 入口（thin wrapper）
│   ├── cli.py                     # CLI 参数解析与命令分发
│   ├── daemon.py                  # 守护循环与 runtime action 执行
│   ├── config.py                  # 配置读写 + 状态管理
│   ├── srun_auth.py               # SRun 认证协议实现
│   ├── crypto.py                  # 加密算法（自定义 Base64、HMAC、BX1）
│   ├── network.py                 # HTTP 客户端（urllib/wget/uclient-fetch）
│   ├── wireless.py                # WiFi STA 配置管理
│   ├── orchestrator.py            # 登录/登出编排逻辑
│   ├── snapshot.py                # 运行时快照
│   └── schools/
│       ├── __init__.py            # 学校配置自动发现
│       ├── _base.py               # SchoolProfile 基类
│       └── jxnu.py                # 江西师范大学(默认)配置
├── usr/lib/lua/luci/
│   ├── controller/smart_srun.lua  # LuCI 路由与接口
│   ├── model/cbi/smart_srun.lua   # LuCI 配置页
│   └── smart_srun/schema.lua      # LuCI 共享配置 schema / key 集合
└── www/luci-static/resources/
    └── smart_srun.js              # LuCI 页面交互脚本
```

其中，LuCI 页面现在拆成了 "Lua 负责渲染与接口，`smart_srun.js` 负责前端交互" 的结构。打包时 `luci-app-smart-srun` 和 `luci-app-smart-srun-bundle` 都会一起包含 `schema.lua` 和静态 JS 资源。`scripts/` 与 `tests/` 属于开发态文件，不会进入最终 ipk 包。

### 适配其他学校

学校适配现在有两种模式：

- **legacy `Profile`**：只适合"换一组元数据 + 换协议常量"的学校。你继承 `SchoolProfile`，覆盖 `ALPHA`、`DEFAULT_BASE_URL`、运营商列表这些静态参数，登录/登出/状态查询仍走内置默认实现。除了类属性，`_base.py` 里的方法（如 `build_username`、`build_login_params`、`parse_login_response`、API 路径等）也都可以 override。
- **full runtime mode**：适合学校需要自定义登录流程、状态探测、CLI 扩展、守护循环钩子，或者要给 LuCI 暴露动态学校字段时使用。入口可以是 `build_runtime(core_api, cfg)`，也可以是 `Runtime(core_api, cfg)`。runtime 可以实现 15 个 boundary methods（如 `login_once`、`get_cli_commands`、`daemon_before_tick` 等），未实现的会自动回落到默认行为；同时会收到 `core_api` 字典，内含 `crypto.*` 和默认登录/登出等 12 个可复用函数。详见 `school_runtime.py`。

运行时解析顺序固定如下：

1. 模块定义了 `build_runtime(core_api, cfg)`，优先用它。
2. 否则如果定义了 `Runtime(core_api, cfg)`，实例化这个类。
3. 再否则如果只有 `Profile`，自动包一层兼容适配器，回落到 legacy 模式。
4. `school` 为空或显式为 `default` 时，使用内置默认 runtime。

#### legacy `Profile` 示例

在 `root/usr/lib/smart_srun/schools/` 下新建 Python 文件，继承 `SchoolProfile` 并填写学校参数：

```python
from _base import SchoolProfile


class Profile(SchoolProfile):
    NAME = "XX大学"
    SHORT_NAME = "xxu"
    DESCRIPTION = "XX大学深澜认证配置"
    CONTRIBUTORS = ("@your_github",)

    ALPHA = "..."           # 深澜自定义 Base64 字母表
    DEFAULT_BASE_URL = "http://x.x.x.x"
    DEFAULT_AC_ID = "1"

    OPERATORS = (
        {"id": "cmcc", "label": "中国移动", "verified": False},
        {"id": "ctcc", "label": "中国电信", "verified": False},
        {"id": "cucc", "label": "中国联通", "verified": False},
    )
    NO_SUFFIX_OPERATORS = ()
```

#### full runtime 元数据约定

full runtime 模块必须提供 `SCHOOL_METADATA`。下面这些字段是稳定契约，`srunnet schools`、配置加载和 LuCI 渲染都会依赖它们：

- `short_name`：学校唯一短名，也是配置里的 `school` 值
- `name`：展示名称
- `description`：补充说明
- `contributors`：贡献者列表
- `operators`：运营商列表，结构与 legacy `Profile.OPERATORS` 保持一致
- `no_suffix_operators`：无需拼接 `@operator` 的运营商 ID 列表
- `capabilities`：可选，声明 runtime 提供的能力标签

注意：`status`、`login`、`logout`、`config` 等核心命令名是保留的，runtime 不能注册同名自定义命令。CLI 钩子须返回 `(handled, exit_code, message)`，daemon 钩子须返回 `(ok, message)` 或 `None`。

如果 runtime 需要学校私有字段，统一放在 `SCHOOL_METADATA["school_extra"]`（或兼容别名 `school_extra_descriptors`）里声明描述符。这里的所有权边界也很死板：

- `school_extra` 这块命名空间归学校 runtime 自己负责设计
- 核心层只负责按描述符做校验、归一化、持久化，以及把支持的字段渲染到 LuCI
- 未声明的 key 会被丢弃，别把学校私有状态偷偷塞进顶层配置
- 运行时特有开关要进 `school_extra`，通用配置继续走现有顶层字段

一个 full runtime 示例，展示了复用默认登录、自定义 CLI 命令和守护循环钩子：

```python
from school_runtime import RUNTIME_API_VERSION


SCHOOL_METADATA = {
    "short_name": "xxu-runtime",
    "name": "XX大学运行时版",
    "description": "需要额外运行时逻辑",
    "contributors": ["@your_github"],
    "operators": [
        {"id": "xn", "label": "校园网", "verified": True},
        {"id": "cmcc", "label": "中国移动", "verified": False},
    ],
    "no_suffix_operators": ["xn"],
    "capabilities": ["status", "daemon"],
    "school_extra": [
        {
            "key": "domain",
            "type": "string",
            "label": "Portal 域名",
            "required": True,
            "default": "portal.example.edu",
        }
    ],
}


class Runtime(object):
    def __init__(self, core_api, cfg):
        self.core_api = core_api
        self.runtime_api_version = RUNTIME_API_VERSION
        self.declared_capabilities = ("status", "daemon")

    # -- 复用默认登录流程 --
    def login_once(self, app_ctx):
        # 登录前做点自己的事，然后委托给默认实现
        return self.core_api["default_login_once"](app_ctx)

    # -- 自定义在线检测 --
    def query_online_status(self, app_ctx, expected_username=None, bind_ip=None):
        return False, "离线"

    # -- 注册自定义 CLI 命令: srunnet info --
    def get_cli_commands(self):
        return [{"name": "info", "help": "显示学校特有信息"}]

    def handle_cli_command(self, app_ctx, args):
        if args and args[0] == "info":
            domain = app_ctx["cfg"].get("school_extra", {}).get("domain", "未配置")
            print("Portal: " + domain)
            return True, 0, ""
        return False, 0, ""

    # -- 守护循环钩子：每次 tick 前执行 --
    def daemon_before_tick(self, app_ctx, state, interval):
        # 返回 None 表示不干预，返回 (ok, message) 可以跳过本轮或记录日志
        return None
```

### GitHub Actions 一键编译

仓库内置两个工作流：

| 工作流                  | 用途                               |
| ----------------------- | ---------------------------------- |
| `pre-release build`     | 开发预览构建，可选发布 pre-release |
| `Version Release Build` | 正式版本构建 + 发布                |

在 GitHub 页面进入 **Actions**，选择对应工作流，点击 **Run workflow** 即可构建。

构建产物包含：

- `smart-srun_*.ipk`
- `luci-app-smart-srun_*.ipk`
- `luci-app-smart-srun-bundle_*.ipk`

其中：

- `smart-srun` + `luci-app-smart-srun` 是标准 split 包，适合通过 LuCI / opkg 管理和升级
- `luci-app-smart-srun-bundle` 是单文件手动安装包，适合没有下载源、一键安装的场景

放入后重启服务即可在 LuCI 中选择。欢迎提交 PR 分享你的学校配置！

## License
WTFPL
