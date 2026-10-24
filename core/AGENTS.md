# Go 核心

本目录是独立 Go module。先读涉及的包及相邻测试；公开架构说明见[文档站](https://srun-doc.guiguisocute.com/development/architecture)。

## 代码地图

| 责任 | 入口 |
| --- | --- |
| 命令、进程与本地 RPC | [cmd/srunnet](cmd/srunnet/)、[cli](internal/cli/)、[daemon](internal/daemon/)、[control](internal/control/) |
| 状态、调度与用户动作 | [domain](internal/domain/)、[application](internal/application/)、[policy](internal/policy/) |
| 配置、备份和秘密 | [config](internal/config/)；`ParseBackup` 位于 [backup.go](internal/config/backup.go) |
| 登录事务与 SRun 编码 | [auth](internal/auth/)、[protocol/srun](internal/protocol/srun/) |
| 网口绑定、联网检测与 portal | [transport](internal/transport/)、[openwrt](internal/openwrt/)、[portal](internal/portal/) |
| 无线观察与切换回滚 | [wifi](internal/wifi/)、[wireless](internal/wireless/) |
| 学校目录与声明式策略 | [presets](internal/presets/)、[strategy](internal/strategy/) |
| 更新 Worker 与恢复 | [update](internal/update/)；[部署手册](../doc/development/deployment.md) |

## 容易误用的约束

- 配置是 `schema_version: 2` 强类型 JSON；启动不读 Python 旧路径。显式备份导入走预览、revision/CAS 和动作互斥，导入后自动认证保持关闭。
- 账号的 `operator_suffix` 决定登录后缀：空串不追加，非空追加对应值。空值和未选择不同，不猜运营商后缀。
- 认证策略声明参数和扩展字段，不自行创建 HTTP 客户端。保留命令的唯一集合是 [strategy.ReservedCommands](internal/strategy/strategy.go)，不要在文档复制列表。
- 手动动作、自动维护和探测使用同一选择网口的 IPv4/L3 绑定；缺少严格绑定时失败关闭，不退回更弱 HTTP 路径。
- 例行状态不得覆盖动作终态的 `last_action_message` / `last_action_portal_url`；新动作入队时清空。Portal 可达但 Internet 探测失败与认证拒绝分开表示。
- 静默时段覆盖受管有线账号及活动账号；强制下线去重，只重试失败项。状态投影不能意外触发网络操作。
- 学校预设页面读取不联网；默认每日更新时间为北京时间 09:00，tmpfs 日领取记录跨服务重启保留、设备重启后不保留。
- 预设与配置跨层修改同时读 [doc/AGENTS.md](../doc/AGENTS.md) 和 [root/AGENTS.md](../root/AGENTS.md)。升级与恢复使用原生受信验签，不沿用手动安装的信任绕过。
- 日志与错误不得包含密码、真实备份或原始认证响应体。

## 验证与历史规格

相关包测试从本目录运行 `go test ./internal/<package>`；完整 Linux 门禁和硬件边界见 [tests/README.md](../tests/README.md)。
更改依赖方向时检查 [tests/architecture](tests/architecture/)，不能仅凭编译判断架构保持。

冻结设计材料位于本地 `.codex/go-rewrite/`（02 架构、03 契约、04 状态/网络、05 测试、15 改进契约），任务记录在 `.codex/go-loop/`。
它们被忽略，克隆不会带来；已提供时按当前任务读取相关章节，缺失时说明限制，不声称完成依赖这些材料的验收。
旧交接的运行状态不能代替当前观察。
