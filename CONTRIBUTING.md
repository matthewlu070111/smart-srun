# 贡献者指南

本仓库是 OpenWrt 包源码，不是普通单体应用。真正随包安装的内容都在 `root/` 下；`tests/`、`scripts/` 和 `doc/` 只用于开发、验证和维护。

## 项目结构

```text
root/
├── etc/init.d/smart_srun          # procd 服务脚本
├── usr/bin/srunnet                # CLI 入口脚本
├── usr/lib/smart_srun/
│   ├── client.py                  # thin wrapper，注入 sys.path 后进入 CLI
│   ├── cli.py                     # argparse 与命令分发
│   ├── daemon.py                  # 守护循环、账号/热点管理、runtime action
│   ├── config.py                  # JSON 配置、迁移、状态与 runtime 字段归一化
│   ├── crypto.py                  # SRun BX1、xencode、自定义 base64、sha1/md5
│   ├── logger.py                  # 结构化日志、阈值过滤、轮转
│   ├── network.py                 # urllib/wget/uclient-fetch HTTP 客户端
│   ├── portal_detect.py           # AC_ID/Portal 参数探测
│   ├── school_presets.py          # 远端学校预设加载、缓存和归一化
│   ├── srun_auth.py               # 默认 SRun 登录/登出/在线查询流程
│   ├── school_runtime.py          # 学校 runtime 契约与兼容适配器
│   ├── updater.py                 # LuCI/CLI 自动更新逻辑
│   └── schools/
│       ├── __init__.py            # 学校模块自动发现与元数据
│       ├── _base.py               # legacy SchoolProfile 基类
│       └── jxnu.py                # 默认 legacy Profile
├── usr/lib/lua/luci/
│   ├── controller/smart_srun.lua  # LuCI endpoints、日志翻译、动作接口
│   ├── model/cbi/smart_srun.lua   # LuCI 配置页 SSR
│   └── smart_srun/schema.lua      # LuCI 共享 schema/key 集合
└── www/luci-static/resources/
    └── smart_srun.js              # LuCI 前端，纯 ES5，无构建步骤
```

开发态文件：

- `tests/`：Python 回归测试，包括源码级 Lua/JS/打包契约检查。
- `scripts/hot_update.py`：开发态热更新脚本，显式上传 shipped 文件。
- 网页登录诊断与学校预设采集油猴脚本：独立仓库 [guiguisocute/smart_srun_school_preset_capture.user](https://github.com/guiguisocute/smart_srun_school_preset_capture.user)，本仓库不再保留副本。
- `doc/school-presets.json`：机器可读学校预设主文件。
- `srun.edu-publish.site` 学校预设镜像站代码：独立仓库 [guiguisocute/smart_srun-_cloudflare_pages](https://github.com/guiguisocute/smart_srun-_cloudflare_pages)，本仓库不再保留副本。

## 运行环境约束

- 设备端只依赖 OpenWrt `python3-light`，shipped Python 必须保持标准库-only。
- 模块使用裸 import，例如 `import orchestrator`、`from config import load_config`。`client.py` 和 `schools/__init__.py` 负责注入路径，不要改成 package-relative imports。
- 持久化配置是 UCI 风格字符串，布尔值通常是 `"0"` / `"1"`，数字也以字符串保存。
- LuCI 前端保持纯 ES5、手写 DOM 与 `XMLHttpRequest`，不引入 bundler、npm 运行时或前端依赖。
- `school_extra` 是 runtime 私有命名空间；未知 key 会被归一化丢弃，不要偷偷塞顶层配置。
- `Makefile` 的 `Build/Compile` 为空是正常的；真正构建由 OpenWrt SDK 完成。

## 提交前验证

建议本机安装：

```sh
python -m pip install pytest ruff paramiko
```

常用验证：

```sh
python -m pytest tests/ -v
python -m pytest tests/ -k runtime -v
ruff check root/usr/lib/smart_srun/
node --check root/www/luci-static/resources/smart_srun.js
python -m pytest tests/test_lua_syntax.py -v
```

学校门户相关测试的占位地址统一在 `tests/_portal_urls.py`。如果你要用本校网关做本地字符串/解析验证，不要逐个改测试文件，设置环境变量即可：

```sh
export SMARTSRUN_TEST_PORTAL_ORIGIN=http://portal.example.edu
export SMARTSRUN_TEST_DEFAULT_BASE_URL=http://172.17.1.2
export SMARTSRUN_TEST_PORTAL_HTTPS_ORIGIN=https://portal.example.edu
export SMARTSRUN_TEST_PORTAL_IPV4_ORIGIN=http://198.51.100.10
export SMARTSRUN_TEST_PORTAL_BARE_HOST=203.0.113.5
```

这些变量只影响测试占位字符串；单元测试不会因此主动访问真实校园网认证服务，除非某个测试明确 mock/调用网络层。

如果改了 shipped Python / Lua / JS 文件，优先补 source-level 回归测试。这个项目很多行为只能在路由器上完整验证，所以本地测试经常直接检查 LuCI endpoint、按钮 ID、配置 schema、打包规则、脚本源码契约或 JSON schema。

## 路由器热更新

```sh
export SMARTSRUN_ROUTER_PASSWORD=<password>
export SMARTSRUN_ROUTER_HOST=10.0.0.1
python scripts/hot_update.py --dry-run
python scripts/hot_update.py --probe
python scripts/hot_update.py
```

`--dry-run` 只打印计划。`--probe` 会上传到 `/tmp/smart_srun_probe_*` 并做远端语法/import 冒烟检查，不覆盖生产路径。不带参数会覆盖路由器文件、清缓存并重启相关服务。

注意：`scripts/hot_update.py` 维护显式上传列表，不做通配扫描。新增 shipped Python / Lua / JS / JSON 文件时必须手动加入上传列表，否则本地测试通过、路由器却缺文件。

## 学校预设

预设主文件是 `doc/school-presets.json`，随包兜底文件是 `root/usr/lib/smart_srun/school_presets_fallback.json`。插件运行时读取顺序：

1. `https://srun.edu-publish.site/school-presets.json`
2. `https://raw.githubusercontent.com/matthewlu070111/smart-srun/main/doc/school-presets.json`
3. 本地缓存 `school_presets_cache.json`
4. 随包兜底 `school_presets_fallback.json`

只把确认可用的学校标成 `active`；未测通或信息不完整的学校保持 `draft`。

预设中的运营商后缀只写在 `operators[].suffix` 中（旧的 `operators[].id` 仍兼容读取）。纯账号/无后缀使用空字符串表示：

```json
{"suffix": "", "label": "校园网"}
```

如果只知道某运营商存在、但还没确认它的真实后缀，用 `"??"` 占位表示“未验证”：

```json
{"suffix": "??", "label": "中国移动"}
```

前端会对 `"??"` 给出未验证提示，且绝不会把 `"??"` 直接写进账号；后端归一化时会把存到账号里的 `"??"` 当作空后缀，避免拼出 `user@??`。

不要新增 `no_suffix_operators`，也不要用 `xn` 表示空后缀。`defaults` 只保存环境默认值：

- `base_url`
- `ac_id`
- `ssid`
- `access_mode`

不要在学校预设 `defaults` 里写 `operator` 或 `operator_suffix`。账号实际登录时使用哪个后缀，由 LuCI/CLI 保存到账号级 `operator` 和 `operator_suffix`；后缀框填什么，最终用户名就拼什么，留空就不拼。

`observed_login_shape` 只记录真实网页登录请求中抓到的字段，不要为了“看起来完整”伪造：

- `n`
- `type`
- `enc`
- `info_prefix`
- `double_stack`
- `os`
- `name`

更新预设后同步随包 fallback：

```sh
python -c "import json, pathlib; p=json.loads(pathlib.Path('doc/school-presets.json').read_text(encoding='utf-8')); p['source']='bundled fallback'; pathlib.Path('root/usr/lib/smart_srun/school_presets_fallback.json').write_text(json.dumps(p, ensure_ascii=False, indent=2)+'\n', encoding='utf-8')"
```

同步 fallback 时只允许 `source` 不同，内容应与 `doc/school-presets.json` 保持一致。

## 油猴采集脚本

学校预设和登录形态字段应优先来自油猴采集脚本。脚本**独立维护在单独的仓库**：[guiguisocute/smart_srun_school_preset_capture.user](https://github.com/guiguisocute/smart_srun_school_preset_capture.user)，本仓库不再保留副本，改动请提交到那边。脚本会捕获 URL query、POST/form body、登录响应和可解码的 `{SRBX1}` `info`，但不会导出明文账号、密码、challenge 或加密 `info` 原文。

登录成功后，脚本会进入“提交信息，协助开发者”流程，生成预设 JSON 并引导用户提交 Issue（对终端用户而言 PR 太麻烦，脚本只保留提交 Issue 入口）。登录失败时只显示已捕获字段和诊断信息，鼓励用户修正账号密码或网络状态后重试。

脚本会跨多次登录尝试累积运营商后缀：因为后缀在登录请求里就能抓到，即便这次登录失败也会被记录。用户用不同运营商账号多试几次，就能把一所学校的运营商后缀一次抓全，导出的 `operators` 更完整。面板会显示“已抓取哪些运营商后缀”的提示。遵循“抓一个补一个”原则：脚本只导出真正抓到过的运营商，不会预设某学校一定有三大运营商，也不会主动注入 `"??"` 占位（`"??"` 是维护者手工标注“已知存在但后缀未验证”的约定）。

`status`（active/draft/deprecated）是维护者评审时标注的 tag，不在采集脚本里让终端用户填写：脚本按登录是否成功给出 active/draft 初值，维护者在评审 Issue 时再调整。

## 适配其他学校

先判断是否真的需要写 runtime：

- 只是不知道网关、AC_ID、SSID、运营商后缀或登录形态字段：优先维护 `doc/school-presets.json`。
- 只换静态参数、API 路径、ALPHA 或解析细节：使用 legacy `Profile`。
- 登录流程、在线检测、CLI 命令、守护循环、学校私有字段需要自定义：使用 full runtime。

### 深澜版本差异

不同学校的深澜页面可能差异很大，但第一轮不要直接手写新算法。先用油猴脚本确认真实网页登录请求：

- 登录 API 路径是否仍是 `/cgi-bin/srun_portal`
- challenge API 是否仍是 `/cgi-bin/get_challenge`
- `n`、`type`、`enc` 是否与默认 `200/1/srun_bx1` 不同
- `info` 前缀是否是 `{SRBX1}`
- `double_stack` 是否出现且取值是否不同
- `os`、`name` 是否影响登录
- 用户名是否真的带 `@后缀`
- 在线查询是否能用 `/cgi-bin/rad_user_info`
- 认证页是否是 HTTPS；如果 HTTPS 请求失败，应提示安装 Python SSL 相关依赖后再试

如果只是这些字段不同，优先通过账号级高级登录参数和学校预设解决。只有出现完全不同的加密、签名、状态接口或登录流程时，再写 full runtime。

### legacy Profile

在 `root/usr/lib/smart_srun/schools/` 下新建 Python 文件：

```python
from _base import SchoolProfile


class Profile(SchoolProfile):
    NAME = "XX大学"
    SHORT_NAME = "xxu"
    DESCRIPTION = "XX大学深澜认证配置"
    CONTRIBUTORS = ("@your_github",)

    ALPHA = "..."  # 深澜自定义 base64 字母表；不确定就先别改
    DEFAULT_BASE_URL = "http://portal.example.edu"
    DEFAULT_AC_ID = "1"
    DEFAULT_N = "200"
    DEFAULT_TYPE = "1"
    DEFAULT_ENC = "srun_bx1"
    DEFAULT_INFO_PREFIX = "SRBX1"
    DEFAULT_DOUBLE_STACK = "0"
    DEFAULT_LOGIN_OS = "Windows 10"
    DEFAULT_LOGIN_NAME = "Windows"

    OPERATORS = (
        {"suffix": "cmcc", "label": "中国移动"},
        {"suffix": "ctcc", "label": "中国电信"},
        {"suffix": "cucc", "label": "中国联通"},
        {"suffix": "", "label": "校园网"},
    )
```

`SchoolProfile` 的方法也可以覆盖，例如 `build_urls()`、`build_login_params()`、`parse_login_response()`、`parse_online_status()`。但如果只是账号级 `n/type/enc/info_prefix/double_stack/os/name` 差异，优先让用户在账号高级参数里配置，不要复制一份默认登录流程。

### full runtime

full runtime 模块必须提供 `SCHOOL_METADATA`，入口可以是 `build_runtime(core_api, cfg)` 或 `Runtime(core_api, cfg)`。解析顺序固定：

1. `build_runtime(core_api, cfg)`
2. `Runtime(core_api, cfg)`
3. `Profile`
4. 内置 default runtime

稳定元数据字段：

- `short_name`：学校唯一短名，也是配置里的 `school` 值。
- `name`：展示名称。
- `description`：补充说明。
- `contributors`：贡献者列表。
- `operators`：运营商后缀列表，`suffix` 就是后缀字符串（旧的 `id` 仍兼容读取），空字符串表示不拼后缀，`"??"` 表示后缀未验证。
- `capabilities`：可选，声明 runtime 能力标签。
- `school_extra` / `school_extra_descriptors`：学校私有字段描述符。

不要使用 `no_suffix_operators`。不要把运营商后缀放进学校 metadata 的 `defaults.operator_suffix`。

保留命令名不能被 runtime 注册为自定义命令：`status`、`login`、`logout`、`relogin`、`daemon`、`schools`、`config`、`switch`、`log`、`enable`、`disable`、`help`、`man`、`update`、`presets`、`detect`。

CLI 钩子返回 `(handled, exit_code, message)`。daemon 钩子返回 `(ok, message)` 或 `None`。runtime action 返回 `(ok, message)`。

示例：

```python
from school_runtime import RUNTIME_API_VERSION


SCHOOL_METADATA = {
    "short_name": "xxu-runtime",
    "name": "XX大学运行时版",
    "description": "需要额外运行时逻辑",
    "contributors": ["@your_github"],
    "operators": [
        {"suffix": "", "label": "校园网"},
        {"suffix": "cmcc", "label": "中国移动"},
        {"suffix": "hcmcc", "label": "移动特殊后缀"},
    ],
    "capabilities": ["status", "daemon"],
    "school_extra": [
        {
            "key": "portal_domain",
            "type": "string",
            "label": "Portal 域名",
            "required": False,
            "default": "",
            "description": "仅在该学校 runtime 需要额外域名时填写",
        },
        {
            "key": "strict_online_check",
            "type": "bool",
            "label": "严格在线检测",
            "default": False,
        },
    ],
}


class Runtime(object):
    def __init__(self, core_api, cfg):
        self.core_api = core_api
        self.runtime_api_version = RUNTIME_API_VERSION
        self.declared_capabilities = ("status", "daemon")

    def login_once(self, app_ctx):
        # 复用默认登录，只在必要时前后加学校私有逻辑。
        return app_ctx["core_api"]["default_login_once"](app_ctx)

    def query_online_status(self, app_ctx, expected_username=None, bind_ip=None):
        # 可以自定义在线检测；能复用默认逻辑时优先复用。
        return app_ctx["core_api"]["default_query_online_status"](
            app_ctx,
            expected_username=expected_username,
            bind_ip=bind_ip,
        )

    def get_cli_commands(self):
        return [{"name": "school-info", "help": "显示学校运行时信息"}]

    def handle_cli_command(self, app_ctx, args):
        if args and args[0] == "school-info":
            print(app_ctx["cfg"].get("school_extra", {}))
            return True, 0, ""
        return False, 0, ""

    def daemon_before_tick(self, app_ctx, state, interval):
        return None
```

建议测试：

```sh
python -m pytest tests/test_school_runtime_loader.py -v
python -m pytest tests/test_school_runtime_dispatch.py -v
python -m pytest tests/test_school_runtime_config.py -v
python -m pytest tests/test_school_runtime_cli.py -k runtime -v
```

## LuCI 日志面板注意事项

日志面板不是单点实现，下面几个地方绑定在一起：

- `root/usr/lib/lua/luci/controller/smart_srun.lua` 的 `action_log_tail()` 同时服务主日志面板和动作进度弹窗；默认行为必须继续兼容 `channel=plugin` + `since=...`。
- 友好日志渲染在 controller 的 `friendly_line()` / `friendly_log_text()`；`model/cbi/smart_srun.lua` 的首屏 SSR 也复用它。
- 新增 structured log event 时，如果要在 LuCI 显示中文，同步更新 controller 里的 `event_zh`。
- 前端颜色判断靠 `[错误]`、`[警告]`、`[调试]`、`[信息]` 前缀，不能随意删。

## GitHub Actions 构建

仓库内置两个工作流：

| 工作流 | 用途 |
| --- | --- |
| `pre-release build` | 开发预览构建，可选发布 pre-release |
| `Version Release Build` | 正式版本构建并发布 draft release |

构建会并行产出 opkg 用 `.ipk` 和 apk 用 `.apk`。Release 资产规则由 `scripts/release_assets.py` 控制：一个 bundle ipk、一个 bundle apk，以及一个包含 split 包的 zip。

`Makefile` 中 checked-in 的 `PKG_VERSION` 保持 `0.0.0`；工作流运行时临时 patch 版本号。
