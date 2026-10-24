# LuCI 与设备安装资源

安装范围由 [Makefile](../Makefile) 的 `SmartSrun/InstallCore` 和 `SmartSrun/InstallLuCI` 决定。
指南不是设备运行文件；新增安装资源同时核对 Makefile 和包契约测试。

## 入口

- [controller](usr/lib/lua/luci/controller/smart_srun.lua)：HTTP 动作、友好日志与通道过滤。
- [CBI model](usr/lib/lua/luci/model/cbi/smart_srun.lua)：表单与 SSR 日志面板。
- [RPC/bridge/schema](usr/lib/lua/luci/smart_srun/)：Go socket 协议、表单转换与投影。
- [smart_srun.js](www/luci-static/resources/smart_srun.js)：原生 ES5、XMLHttpRequest 和 DOM 更新。
- [init scripts](etc/init.d/)：主服务与独立更新 Worker；[keep.d](lib/upgrade/keep.d/smart-srun)：只保留用户配置与自定义预设。

## 界面与操作契约

- 既有布局、字段、中文文案与五步向导冻结；只替换后端调用，不引入框架、bundler 或运行时 JS 依赖。
- 已批准的例外是进阶设置的每日预设更新开关、24 小时时间选择与手动刷新。读取页面不触发远程刷新；调度见 [core/AGENTS.md](../core/AGENTS.md)。
- 向导五步分别为接入网络、认证地址、被动后缀发现、凭据、确认。任意后缀、空后缀与未选择状态不能合并，不生成猜测候选。
- 临时无线配置通过 Go 无线事务提交或回滚；LuCI、RPC 与运行时的取消/超时结果必须一致。
- 保存后可显示可选 Issue 草稿，只含已审阅环境参数和后缀，不含凭据或设备标识。保存不证明登录成功或全校适配，只有用户提交 Issue。
- 密码眼睛按钮保持可访问性；详细诊断与状态摘要分开。备份导入保留预览与 revision 冲突保护。

## 日志约定

- `action_log_tail()` 的主面板使用显式 `channel`；进度弹层仍依赖默认 `channel=plugin` 和 `since`。
- 翻译在 controller 的 `friendly_line()` / `friendly_log_text()`，CBI SSR 直接复用；新增事件同步 `event_zh`。
- JS 颜色依赖 `[错误]`、`[警告]`、`[调试]`、`[信息]` 前缀，不只改单边。
- 动作终态与例行状态分开，约束见 [core/AGENTS.md](../core/AGENTS.md)。

语法与 Lua/JS 契约、交互测试入口在 [tests/README.md](../tests/README.md)。
不要将旧 Python 的逐文件热更新清单套到当前 SDK 包部署。
