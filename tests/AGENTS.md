# 主机与 LuCI 测试

[README.md](README.md) 是命令、依赖和行为映射入口；设备核心测试在 [core](../core/)。

- 源码级 Lua/JS 契约检查是正常手段，真实交互仍需对应 fixture 或设备证据。
- fixture 只用合成凭据和环境参数，不复制真实配置、备份或认证响应。
- 缺少 Lua、Node、Linux 或设备环境时记录缺失项，跳过不能写成通过。
- 包布局以 [Makefile](../Makefile) 为准；新增文件必须检查实际安装范围。
- 预设 `doc_url` 可指向旧入口；移动文档前检查 [test_preset_catalogue.py](test_preset_catalogue.py)。
- 优先验证行为和危险副作用，不用脆弱的全文快照锁死文档措辞。
