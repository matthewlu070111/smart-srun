package cli

import (
	"fmt"
	"os"
)

// WriteHelp documents the command and input contracts.
func WriteHelp(out *os.File) {
	fmt.Fprint(out, `SMART SRun (srunnet) —— OpenWrt 深澜校园网认证客户端

配置保存与认证命令共用后台服务；状态和配置读取不会启动服务。

可用命令
  status [--json]          显示状态；不会启动服务
  login|logout|relogin [ID] [--json] [--no-wait] [--ignore-quiet]
                           省略 ID 使用当前校园账号；默认等到动作结束
                           logout 暂停该账号自动认证，手动登录成功后恢复
  switch campus|hotspot [ID] [--json] [--no-wait] [--ignore-quiet]
                           省略 ID 使用对应默认项；成功后保存当前选择
  enable|disable           保存自动认证开关（JSON 结果）
  service ensure-running   启动本项目服务并等待就绪（最多 5 秒）
  service stop             取消进行中的动作并停止本项目服务
  service status           只报告服务是否在运行
  daemon                   在前台运行服务（由 procd 调用）
  config validate [文件]   校验配置；不带文件则读标准输入
  config schema            输出只读字段契约（JSON）
  config defaults          输出默认配置（JSON）
  config show|get [字段路径] 读取已保存配置，隐藏密码（JSON）
  config set               从 JSON 标准输入保存设置
  config account|hotspot list|get ID
                           读取账号或热点，隐藏密码（JSON）
  config account|hotspot add|edit|rm|default
                           从 JSON 标准输入保存账号或热点（JSON 结果）
  config account|hotspot add|edit [ID] --interactive
                           终端交互输入；密码不回显，编辑时留空保留
                           TTY 中 add/edit 自动进入交互；管道仍读取 JSON
  schools list|inspect ID [--json]  查看内置认证策略（不联网）
  presets list [--all --json]      查看本地学校参数预设
  presets inspect ID [--json]      查看一个学校参数预设
  presets refresh --iface 接口 [--json --no-wait]
                           从指定出口刷新；失败时保留本地目录
  detect env|acid|operators|identity --access-mode wired|wifi --iface 接口
          [--base-url URL --ac-id ID --ssid SSID --user-id 账号 --json --no-wait]
                           探测环境、地址、页面后缀或只读在线身份
  detect operator|verify --stdin [--json --no-wait]
                           从 JSON 标准输入读取凭据与最多五个已知后缀
  detect status|cancel 任务ID      查询或取消任务（JSON）
  detect wifi start --stdin [--json --no-wait]
                           临时连接 Wi-Fi，15 分钟内保存或自动恢复
  detect wifi status|cancel 任务ID [--json]
  detect wifi account|commit --stdin
                           用无线任务结果保存账号并确认连接
  log [tail|follow] [-n N] [--channel plugin|network]
                           默认跟随日志；单独 -n N 只读最后 N 行
  log tail [--json]        一次读取日志，JSON 只输出一个结果
  log runtime [--json]     查看当前认证策略与生效参数（隐藏凭据）
  update check [--channel stable|rc] [--no-wait]
                           检查兼容更新，返回已验证的计划 ID（JSON）
  update run 计划ID [--background]
                           独立安装任务；停止主服务不会中断安装
  update status           从固定快照读取进度，不启动服务（JSON）
  update inventory        读取实际安装包、架构和固件系列（JSON）
  update recover [--background]
                           恢复未完成的安装；配置备份独立保留
  update prepare-local     SSH 部署入口；从标准输入读取新旧清单，
                           核验固定 update-inbox 目录中的安装包（JSON）
  version                  显示版本
  help                     显示本帮助
  man                      显示本帮助及输入示例

保存输入示例（revision 来自 config show；冲突后请重新读取，不能盲目覆盖）
  config set: {"expected_revision":0,"settings":{"enabled":false}}
  config account add: {"expected_revision":0,"account":{"user_id":"学生账号","password":"密码","wired_iface":"wan"}}
  config account edit: {"expected_revision":1,"account":{"id":"c1","label":"新名称"}}
  config hotspot add: {"expected_revision":2,"profile":{"ssid":"热点","encryption":"none"}}
  rm/default: {"expected_revision":3,"id":"c1"}
  detect verify --stdin: {"access_mode":"wired","iface":"wan","base_url":"http://认证网关","ac_id":"1","user_id":"学生账号","password":"密码","candidates":[""]}
  candidates 中空字符串表示不加后缀；不自动猜测运营商。已在线时只读取身份，不退出已有会话。
密码省略表示保留，空字符串表示清空；密码通过管道或文件输入，请勿放进命令行。

退出码
  0 成功   2 参数或配置无效   3 服务未运行   4 动作失败
  5 冲突或忙   6 能力不支持   130 用户取消
`)
}
