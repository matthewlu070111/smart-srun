import json
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]
LUA_ROOT = REPO_ROOT / "root" / "usr" / "lib" / "lua" / "luci"
CONTROLLER_FILE = LUA_ROOT / "controller" / "smart_srun.lua"


def lua_string(value):
    return json.dumps(str(value).replace("\\", "/"), ensure_ascii=False)


class LuaSyntaxSmokeTests(unittest.TestCase):
    def test_shipped_luci_lua_files_parse_with_luac(self):
        luac = shutil.which("luac")
        if not luac:
            self.skipTest("luac is not installed")

        lua_files = sorted(LUA_ROOT.rglob("*.lua"))
        self.assertTrue(lua_files, "expected shipped LuCI Lua files")

        for path in lua_files:
            with self.subTest(path=str(path.relative_to(REPO_ROOT))):
                with (
                    tempfile.TemporaryFile(mode="w+t", encoding="utf-8") as stdout,
                    tempfile.TemporaryFile(mode="w+t", encoding="utf-8") as stderr,
                ):
                    result = subprocess.run(
                        [luac, "-p", str(path)],
                        stdin=subprocess.DEVNULL,
                        stdout=stdout,
                        stderr=stderr,
                        text=True,
                    )
                    stdout.seek(0)
                    stderr.seek(0)
                    output = (stderr.read() or stdout.read()).strip()
                self.assertEqual(
                    result.returncode,
                    0,
                    output,
                )

    def test_luci_friendly_line_executes_with_lua(self):
        lua = shutil.which("lua")
        if not lua:
            self.skipTest("lua is not installed")

        with tempfile.TemporaryDirectory() as temp_dir:
            stub_root = Path(temp_dir) / "lua"
            (stub_root / "luci" / "smart_srun").mkdir(parents=True)
            (stub_root / "nixio").mkdir(parents=True)
            (stub_root / "luci" / "http.lua").write_text(
                "\n".join(
                    [
                        "local M = {}",
                        "function M.formvalue() return nil end",
                        "function M.prepare_content() end",
                        "function M.write() end",
                        "function M.status() end",
                        "function M.header() end",
                        "return M",
                    ]
                ),
                encoding="utf-8",
            )
            (stub_root / "luci" / "jsonc.lua").write_text(
                "\n".join(
                    [
                        "local M = {}",
                        "function M.parse() return {} end",
                        "function M.stringify() return '{}' end",
                        "return M",
                    ]
                ),
                encoding="utf-8",
            )
            (stub_root / "luci" / "sys.lua").write_text(
                "\n".join(
                    [
                        "local M = {}",
                        "function M.exec() return '' end",
                        "function M.call() return 0 end",
                        "return M",
                    ]
                ),
                encoding="utf-8",
            )
            (stub_root / "luci" / "util.lua").write_text(
                "\n".join(
                    [
                        "local M = {}",
                        "function M.trim(value) return tostring(value or ''):match('^%s*(.-)%s*$') end",
                        "function M.pcdata(value) return tostring(value or '') end",
                        "return M",
                    ]
                ),
                encoding="utf-8",
            )
            (stub_root / "nixio" / "fs.lua").write_text(
                "\n".join(
                    [
                        "local M = {}",
                        "function M.readfile() return '' end",
                        "function M.writefile() return true end",
                        "function M.access() return false end",
                        "function M.mkdirr() return true end",
                        "function M.remove() return true end",
                        "function M.dir() return function() return nil end end",
                        "return M",
                    ]
                ),
                encoding="utf-8",
            )
            (stub_root / "luci" / "smart_srun" / "schema.lua").write_text(
                "\n".join(
                    [
                        "local M = {}",
                        "M.POINTER_KEYS = {}",
                        "M.LIST_KEYS = {}",
                        "function M.global_scalar_key_set() return {} end",
                        "function M.with_file_lock(_, callback) return callback() end",
                        "function M.installed_package_display_text() return 'Bundle 版 v0.0.0-r1' end",
                        "return M",
                    ]
                ),
                encoding="utf-8",
            )

            script = """
package.path = %s .. "/?.lua;" .. %s .. "/?/init.lua;" .. %s .. "/?.lua;" .. %s .. "/?/init.lua;" .. package.path
_ = function(value) return value end
entry = function() return {} end
call = function(value) return value end
cbi = function(value) return value end
dofile(%s)
local controller = package.loaded["luci.controller.smart_srun"]
assert(controller and controller.friendly_line)
local out = controller.friendly_line('[2026-06-01 22:00:00] INFO http_fetch_result url="http://example/login" status_code=200 duration_ms=123 password=*** | ok')
assert(out:find("URL=http://example/login", 1, true), out)
assert(out:find("状态码=200", 1, true), out)
assert(out:find("耗时=123ms", 1, true), out)
assert(not out:find("password", 1, true), out)
assert(not out:find("***", 1, true), out)
""" % (
                lua_string(stub_root),
                lua_string(stub_root),
                lua_string(LUA_ROOT.parent),
                lua_string(LUA_ROOT.parent),
                lua_string(CONTROLLER_FILE),
            )
            script_path = Path(temp_dir) / "friendly_probe.lua"
            script_path.write_text(script, encoding="utf-8")

            with (
                tempfile.TemporaryFile(mode="w+t", encoding="utf-8") as stdout,
                tempfile.TemporaryFile(mode="w+t", encoding="utf-8") as stderr,
            ):
                result = subprocess.run(
                    [lua, str(script_path)],
                    stdin=subprocess.DEVNULL,
                    stdout=stdout,
                    stderr=stderr,
                    text=True,
                )
                stdout.seek(0)
                stderr.seek(0)
                output = (stderr.read() or stdout.read()).strip()
            self.assertEqual(result.returncode, 0, output)


if __name__ == "__main__":
    unittest.main()
