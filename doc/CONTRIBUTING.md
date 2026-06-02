# 贡献者指南
## 项目结构

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
│   ├── logger.py                  # 结构化日志叶子模块：阈值过滤、上下文传播、timed、append_log 兼容、512 KiB 轮转
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

`logger.py` 是一个刻意保持无内部依赖的 leaf module，`network.py`、`config.py`、`wireless.py` 等都会直接 import 它来避开循环依赖。这里如果再反向依赖别的 `smart_srun` 模块，通常很容易把运行时 import 链搞坏。

## 提交前先这样验证

建议本机准备这些开发工具：

```sh
python -m pip install pytest ruff paramiko
```

Lua / LuCI 相关改动还需要本机能找到 `lua` 和 `luac`。Windows 上可以用 Scoop 安装 Lua 5.1；Linux / WSL 上用发行版包管理器安装即可。

先跑完整测试：

```sh
python -m pytest tests/ -v
```

`pytest.ini` 已把缓存目录改到 `.cache/pytest`，避免旧 `.pytest_cache` 权限异常反复污染测试输出。

只想验证某一块时，可以直接跑：

```sh
python -m pytest tests/test_school_runtime_loader.py -v
python -m pytest tests/ -k runtime -v
ruff check root/usr/lib/smart_srun/
```

测试里包含 OpenWrt / Lua 冒烟检查：`luac -p` 会解析 shipped LuCI Lua 文件，本机 `lua` 会用 stub LuCI 模块执行 `friendly_line()`。如果本机缺少 `luac` 或 `lua`，相关测试会失败，应该先安装工具而不是跳过。

这个仓库很常见的一类改动，是只改了 Lua / JS / 打包脚本，没有很好地做路由器侧自动化。遇到这种情况，补一个 source-level 的 Python 回归测试是正常做法，比如直接断言某个 Lua 端点、按钮 ID、命令字符串或打包规则仍然存在。

## 用热更新脚本做路由器验证

先安装依赖并设置环境变量：

```sh
python -m pip install paramiko
export SMARTSRUN_ROUTER_PASSWORD=<password>
export SMARTSRUN_ROUTER_HOST=10.0.0.1
python scripts/hot_update.py
```

`SMARTSRUN_ROUTER_USER` 和 `SMARTSRUN_LUCI_BASE_URL` 是可选的。

生产热更新前，建议先跑两个低风险检查：

```sh
python scripts/hot_update.py --dry-run
python scripts/hot_update.py --probe
```

`--dry-run` 只打印上传目标和远端命令，不连接路由器，也不需要密码。`--probe` 会把同一批文件上传到 `/tmp/smart_srun_probe_*`，在临时目录里跑 Python 编译 / import、logger、LuCI Lua、init 脚本和 CLI 版本冒烟检查，最后清理探针文件；它不会覆盖生产路径，也不会重启生产服务。

不带参数的 `python scripts/hot_update.py` 是生产热更新：会上传文件到真实路径、跑远端语法检查、清 LuCI / Python 缓存、重启 `smart_srun` 和 `uwsgi`，然后再做 `srunnet status`、`srunnet schools`、`srunnet schools inspect --selected` 和 LuCI 页面验证。这个命令会覆盖路由器文件，只在已经明确确认要部署时执行。

注意：`scripts/hot_update.py` 维护的是一份显式上传列表，不会自动扫描新文件。如果你新增了 shipped Python / Lua / JS 文件，记得把它加进去，不然本地改了、路由器上却没有。

## 改 LuCI 日志面板前先看这里

日志面板不是单点实现，下面几个地方是绑在一起的：

- `root/usr/lib/lua/luci/controller/smart_srun.lua` 的 `action_log_tail()` 同时服务于主日志面板和动作进度弹窗；默认行为必须继续兼容 `channel=plugin` + `since=...` 这条老调用链。
- 友好日志渲染在 controller 里的 `friendly_line()` / `friendly_log_text()`；`model/cbi/smart_srun.lua` 的首屏 SSR 也直接复用它，所以改这里会同时影响首屏和前端轮询结果。
- 如果新增 structured log event，想让 LuCI 显示中文，就要同步更新 controller 里的 `event_zh`。
- 前端颜色判断靠 `[错误]`、`[警告]`、`[调试]`、`[信息]` 这些前缀。只改翻译文案、不保留前缀，颜色就会失效。

## 适配其他学校

学校适配现在有两种模式：

- **legacy `Profile`**：只适合"换一组元数据 + 换协议常量"的学校。你继承 `SchoolProfile`，覆盖 `ALPHA`、`DEFAULT_BASE_URL`、运营商列表这些静态参数，登录/登出/状态查询仍走内置默认实现。除了类属性，`_base.py` 里的方法（如 `build_username`、`build_login_params`、`parse_login_response`、API 路径等）也都可以 override。
- **full runtime mode**：适合学校需要自定义登录流程、状态探测、CLI 扩展、守护循环钩子，或者要给 LuCI 暴露动态学校字段时使用。入口可以是 `build_runtime(core_api, cfg)`，也可以是 `Runtime(core_api, cfg)`。runtime 可以实现 15 个 boundary methods（如 `login_once`、`get_cli_commands`、`daemon_before_tick` 等），未实现的会自动回落到默认行为；同时会收到 `core_api` 字典，内含 `crypto.*` 和默认登录/登出等 12 个可复用函数。详见 `school_runtime.py`。

运行时解析顺序固定如下：

1. 模块定义了 `build_runtime(core_api, cfg)`，优先用它。
2. 否则如果定义了 `Runtime(core_api, cfg)`，实例化这个类。
3. 再否则如果只有 `Profile`，自动包一层兼容适配器，回落到 legacy 模式。
4. `school` 为空或显式为 `default` 时，使用内置默认 runtime。

### legacy `Profile` 示例

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

### full runtime 元数据约定

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

## GitHub Actions 一键编译

仓库内置两个工作流：

| 工作流                  | 用途                               |
| ----------------------- | ---------------------------------- |
| `pre-release build`     | 开发预览构建，可选发布 pre-release |
| `Version Release Build` | 正式版本构建 + 发布                |

在 GitHub 页面进入 **Actions**，选择对应工作流，点击 **Run workflow** 即可构建。

构建产物包含：

- `smart-srun_*.ipk`、`luci-app-smart-srun_*.ipk`、`luci-app-smart-srun-bundle_*.ipk`
- `smart-srun-*.apk`、`luci-app-smart-srun-*.apk`、`luci-app-smart-srun-bundle-*.apk`
- 发布页会附带两个 bundle（`.ipk` 与 `.apk`）以及一个 split 包下载压缩包（zip）

其中：

- `smart-srun` + `luci-app-smart-srun` 是标准 split 包，适合通过 LuCI / opkg（ipk）或 apk（apk）管理和升级
- `luci-app-smart-srun-bundle` 提供单文件手动安装（同时提供 ipk/apk 两种格式），适合没有下载源、一键安装的场景

放入后重启服务即可在 LuCI 中选择。欢迎提交 PR 分享你的学校配置！
