# 贡献者指南

本仓库是 OpenWrt 包源码，不是普通单体应用。真正随包安装的内容都在 `root/` 下；`tests/`、`scripts/`、`doc/` 只用于开发、验证和维护。

## 关键目录

- `root/usr/lib/smart_srun/`：Python 运行时，入口链路是 `client.py -> cli.py -> daemon.py`。
- `root/usr/lib/smart_srun/schools/`：学校 runtime / legacy `Profile`。
- `root/usr/lib/lua/luci/controller/smart_srun.lua`：LuCI 接口、日志翻译和动作端点。
- `root/usr/lib/lua/luci/model/cbi/smart_srun.lua`：LuCI 配置页 SSR。
- `root/www/luci-static/resources/smart_srun.js`：LuCI 前端交互，保持无依赖 ES5。
- `scripts/srun_school_preset_capture.user.js`：网页登录诊断与学校预设采集油猴脚本。
- `scripts/hot_update.py`：开发态热更新脚本，显式维护上传文件列表。

## 提交前验证

建议本机安装：

```sh
python -m pip install pytest ruff paramiko
```

常用验证命令：

```sh
python -m pytest tests/ -v
ruff check root/usr/lib/smart_srun/
node --check root/www/luci-static/resources/smart_srun.js
node --check scripts/srun_school_preset_capture.user.js
python -m pytest tests/test_lua_syntax.py -v
```

学校门户相关测试的占位地址统一在 `tests/_portal_urls.py`。如果你要用本校网关做本地字符串/解析验证，不要逐个改测试文件，直接设置环境变量：

```sh
export SMARTSRUN_TEST_PORTAL_ORIGIN=http://portal.example.edu
export SMARTSRUN_TEST_DEFAULT_BASE_URL=http://172.17.1.2
export SMARTSRUN_TEST_PORTAL_HTTPS_ORIGIN=https://portal.example.edu
export SMARTSRUN_TEST_PORTAL_IPV4_ORIGIN=http://198.51.100.10
export SMARTSRUN_TEST_PORTAL_BARE_HOST=203.0.113.5
```

这些变量只影响测试里的占位字符串；单元测试不会因此主动访问真实校园网认证服务，除非某个测试本身明确 mock/调用了网络层。

如果改了 shipped Python / Lua / JS 文件，优先补 source-level 回归测试。这个项目很多行为只能在路由器上完整验证，所以本地测试通常会直接检查 LuCI endpoint、按钮 ID、配置 schema、打包规则或脚本源码契约。

## 路由器热更新

```sh
export SMARTSRUN_ROUTER_PASSWORD=<password>
export SMARTSRUN_ROUTER_HOST=10.0.0.1
python scripts/hot_update.py --dry-run
python scripts/hot_update.py --probe
python scripts/hot_update.py
```

`--dry-run` 只打印计划。`--probe` 会上传到 `/tmp/smart_srun_probe_*` 并做远端语法/import 冒烟检查，不覆盖生产路径。不带参数会覆盖路由器文件、清缓存并重启相关服务。

注意：`scripts/hot_update.py` 不是通配扫描。新增 shipped 文件时必须手动加入上传列表。

## 学校适配

优先使用远端学校预设。预设文件位于 `doc/school-presets.json`，随包兜底文件是 `root/usr/lib/smart_srun/school_presets_fallback.json`。只把已确认可用的学校标成 `active`；未测通或信息不完整的学校保持 `draft`。

预设中的运营商后缀只写在 `operators[].id` 中。纯账号/无后缀使用空字符串表示，例如：

```json
{"id": "", "label": "校园网"}
```

不要再新增 `no_suffix_operators`，也不要用 `xn` 表示空后缀。学校预设的 `defaults` 只保存 `base_url`、`ac_id`、`ssid`、`access_mode` 这类环境默认值，不再写 `operator` 或 `operator_suffix`；账号实际登录时使用哪个后缀，由 LuCI/CLI 保存到账号级 `operator` 和 `operator_suffix`。

`observed_login_shape` 只记录真实网页登录请求中抓到的字段。不要为了“看起来完整”伪造 `os`、`name`、`n`、`type`、`enc`、`double_stack` 或 `info_prefix`。

legacy `Profile` 适合只替换静态参数的学校：

```python
from _base import SchoolProfile


class Profile(SchoolProfile):
    NAME = "XX大学"
    SHORT_NAME = "xxu"
    DESCRIPTION = "XX大学深澜认证配置"
    CONTRIBUTORS = ("@your_github",)

    DEFAULT_BASE_URL = "http://portal.example.edu"
    DEFAULT_AC_ID = "1"
    OPERATORS = (
        {"id": "cmcc", "label": "中国移动"},
        {"id": "ctcc", "label": "中国电信"},
        {"id": "cucc", "label": "中国联通"},
        {"id": "", "label": "校园网"},
    )
```

需要自定义登录流程、状态探测、CLI 命令或 LuCI 私有字段时，再使用 full runtime，并提供 `SCHOOL_METADATA`。核心命令名如 `status`、`login`、`logout`、`config` 是保留命令，runtime 不能抢占。

## 油猴采集脚本

学校预设和登录形态字段应优先来自 `scripts/srun_school_preset_capture.user.js`。脚本会捕获 URL query、POST/form body、登录响应和可解码的 `{SRBX1}` `info`，但不会导出明文账号、密码、challenge 或加密 `info` 原文。

登录成功后，脚本会进入“提交信息，协助开发者”流程；登录失败时只显示已捕获字段和诊断信息，鼓励用户修正账号密码或网络状态后重试。
