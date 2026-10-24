# 测试入口与验收范围

## 开发主机命令

从仓库根运行。Go 全门禁需要 Linux、Go（版本由 [targets.json](../targets.json) 锁定）和 C 编译器；主机契约检查需要 Python、PyYAML、Lua 5.1、Node 与 GNU make。CI 的实际安装步骤在 [ci.yml](../.github/workflows/ci.yml)。

```sh
bash scripts/verify-go.sh
python3 -m unittest discover -s tests -v
ruff check scripts tests
node --check root/www/luci-static/resources/smart_srun.js
python3 scripts/check_go_source.py
python3 scripts/check_docs.py
```

工作流语法检查使用 CI 固定版本的 actionlint。局部修改先运行对应包或测试文件；文档工具的回归为 `python3 -m unittest discover -s tests -p test_documentation_tools.py -v`。跨层检查可用 `python3 scripts/doc_context.py --base origin/main` 获取建议。

Windows 可运行文档与多数主机测试，不能将缺失 POSIX、Lua 或设备环境的跳过记录写成完整 Linux 门禁通过。

## 行为与证据

The device runtime is Go. Run the complete core gate on Linux with
`bash scripts/verify-go.sh`; it enforces formatting, vet, shuffled tests,
coverage floors and race detection. Device tests are explicit opt-ins and must
not be counted as passed when their environment is absent.

`python3 -m unittest discover -s tests -v` tests the build-host tools, release
metadata, package contracts, preset data and the dependency-free LuCI bridge.
Install Lua 5.1, Node and GNU make on the development host to run every shared
UI and packaging check. Python is not part of the installed package.

The retired Python 1.x runtime and its implementation-specific tests remain in
Git history and on the `1.6.1` branch. Their behavior is covered in the Go tree:

| Behavior | Current regression location |
| --- | --- |
| SRun encoding, hashing and login/logout identity | `core/internal/protocol/srun`, `core/internal/auth` |
| Configuration, secrets, validation and presets | `core/internal/config`, `core/internal/presets` |
| Scheduling, backoff, quiet hours and multi-WAN | `core/internal/application`, `core/internal/policy` |
| Binding, reachability and portal discovery | `core/internal/transport`, `core/internal/openwrt`, `core/internal/portal` |
| Client detection, AP selection and wireless rollback | `core/internal/wifi`, `core/internal/wireless`, `core/internal/daemon` |
| CLI, process lifecycle, logs and update recovery | `core/internal/cli`, `core/internal/daemon`, `core/internal/logstore`, `core/internal/update` |
| LuCI forms, wizard, actions, cancellation and feedback | `tests/lua`, `tests/js`, shared UI tests in this directory |
| Native SDK packages, manifests and release workflows | `test_go_*`, `test_ci_go_release.py`, `core/tests/packaging` |

Removing the obsolete interpreter tests is not evidence of complete parity or
hardware acceptance. Keep unexecuted hardware, RF, campus and soak checks
explicit in release evidence. Test fixtures use synthetic credentials; private
device captures and signing keys do not belong in this tree.
