# 学校目录与文档

用户文章在 [smartsrun-doc](https://github.com/guiguisocute/smartsrun-doc) 维护。
本仓保存随源码维护的[开发手册](development/context.md)、预设源数据、图片与兼容入口。
引用符号名和文件链接，不写易过期行号；验证状态附版本或对应证据。

## 学校预设

- [school-presets.json](school-presets.json) 是公开目录唯一源文件；Makefile 直接安装，Go 树没有待同步的 Python fallback。
- 后缀只放在 `operators[].suffix`：空串是无后缀，`??` 是名称已知但后缀未验证。保留读旧 `operators[].id` 的兼容性。
- 不添加 `defaults.operator`、`defaults.operator_suffix`、`no_suffix_operators` 或 `xn` 空后缀占位，不猜运营商参数。
- 保留未知数据、缺失字段、draft 状态、字段顺序和紧凑/展开格式；不要全量重序列化目录。
- 加载与缓存行为见 [presets](../core/internal/presets/)：坏 JSON/schema 尝试下个来源，新缓存不降级，同日修正可接受。
- [default.md](default.md) 是历史 `doc_url` 兼容入口，不能直接删除；提交方法见[预设贡献指南](https://srun-doc.guiguisocute.com/contribute/presets)。

## 文档更新

- 命令与实现约束随本仓维护；文档站链接到手册，不复制完整流程。
- Python 文章标明 1.x；新用户入口描述当前 Go 行为。
- 安装说明可介绍用户主动选择 `apk add --allow-untrusted`，说明跳过发布者签名验证；不能用于更新器、热部署或发布验收。
- 公共文档只写可复用行为，设备状态、内部计划与私密验收留在 `.codex/`。

预设检查见 [test_preset_catalogue.py](../tests/test_preset_catalogue.py)；
从根目录运行 `python3 scripts/check_docs.py` 检查文档引用与触发映射。
