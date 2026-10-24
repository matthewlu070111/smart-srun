# CI 与发布工作流

- [workflows/ci.yml](workflows/ci.yml) 是主机/Go/文档门禁；普通检查只读仓库，不接触签名密钥或校园账号。
- [workflows/build-go.yml](workflows/build-go.yml) 构建锁定 SDK 矩阵并校验候选。
- [workflows/build-prerelease.yml](workflows/build-prerelease.yml)、[workflows/build-release.yml](workflows/build-release.yml) 与 [workflows/publish-go.yml](workflows/publish-go.yml) 已是 Go 发布流程。
- 签名环境、草稿权限、版本与不可变产物规则在[发布手册](../doc/development/build-release.md)。草稿创建不等于公开发布授权。
- 外部 Actions 固定提交；正式身份不能回退为临时密钥或跳过验证。
- [review-context.json](review-context.json) 由 [doc_context.py](../scripts/doc_context.py) 实际消费；输出阅读材料与建议检查，不自动授权或执行操作。

修改流程后运行 [test_ci_go_release.py](../tests/test_ci_go_release.py) 及工作流语法检查；依赖与命令见 [tests/README.md](../tests/README.md)。
