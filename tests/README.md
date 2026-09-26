# Tests

The device runtime is Go. Run the complete core gate on Linux with
`bash scripts/verify-go.sh`; it enforces formatting, vet, shuffled tests,
coverage floors and race detection. Device tests are explicit opt-ins and must
not be counted as passed when their environment is absent.

`python3 -m unittest discover -s tests -v` tests the build-host tools, release
metadata, package contracts, preset data and the dependency-free LuCI bridge.
Install Lua 5.1, Node and GNU make on the development host to run every shared
UI and packaging check. Python is not part of the installed package.

The retired Python 1.x runtime and its implementation-specific tests remain in
Git history and on the `main` branch. Their behavior is covered in the Go tree:

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
