"""log.file_omit_info: a native checkbox in the log tab, mapped through the bridge."""

import json
from pathlib import Path
import re
import shutil
import subprocess
import unittest

ROOT = Path(__file__).resolve().parents[1]
CBI = ROOT / "root/usr/lib/lua/luci/model/cbi/smart_srun.lua"
BRIDGE = ROOT / "root/usr/lib/lua/luci/smart_srun/bridge.lua"


class LogFileOmitInfoUITests(unittest.TestCase):
    def test_checkbox_is_in_the_log_tab_next_to_the_level(self):
        source = CBI.read_text(encoding="utf-8")
        self.assertIn(
            's:taboption("log", Flag, "log_file_omit_info", "日志文件不记录 INFO 信息"',
            source,
        )
        self.assertIn('log_file_omit_info.default = "1"', source)
        self.assertIn('bind_flag(log_file_omit_info, "log_file_omit_info")', source)
        level = source.index('bind_text(log_level, "log_level")')
        flag = source.index('"log_file_omit_info"')
        text = source.index('"_log_text"')
        self.assertLess(level, flag)
        self.assertLess(flag, text)

    def test_bridge_maps_the_flag_as_a_bool(self):
        source = BRIDGE.read_text(encoding="utf-8")
        self.assertRegex(
            source,
            re.compile(r'log_file_omit_info\s*=\s*\{ path = \{ "log", "file_omit_info" \}, kind = "bool" \}'),
        )

    def test_checkbox_reads_and_writes_the_flat_value(self):
        lua = shutil.which("lua")
        if not lua:
            self.skipTest("lua is not installed")
        script = r'''
local f = assert(io.open(CBI_PATH, 'rb'))
local source = f:read('*a'):gsub('\r\n', '\n'); f:close()
local binders = assert(source:match('(local function bind_flag.-)\nlocal function bind_text'))
local control = assert(source:match('(local log_file_omit_info = .-)\n\nlog_text ='))
for _, case in ipairs({{stored='1', shown='1'}, {stored='0', shown='0'}}) do
    local options = {}
    local s = {taboption=function(self, tab, kind, name, title, description)
        assert(tab == 'log' and kind == 'Flag')
        local opt = {title=title, description=description}
        options[name] = opt; return opt
    end}
    local cfg = {log_file_omit_info=case.stored}
    local env = setmetatable({cfg=cfg, s=s, Flag='Flag',
        set_value=function(k, v) cfg[k] = v end}, {__index=_G})
    local chunk = assert(loadstring(binders .. '\n' .. control)); setfenv(chunk, env); chunk()
    local flag = options.log_file_omit_info
    assert(flag.default == '1' and flag.rmempty == false)
    assert(flag:cfgvalue() == case.shown, 'cfgvalue')
    flag:write('main', '0'); assert(cfg.log_file_omit_info == '0')
    flag:write('main', '1'); assert(cfg.log_file_omit_info == '1')
    flag:remove('main'); assert(cfg.log_file_omit_info == '0')
end
'''
        script = script.replace("CBI_PATH", json.dumps(CBI.as_posix()))
        result = subprocess.run([lua, "-e", script], capture_output=True, text=True, encoding="utf-8", timeout=15)
        self.assertEqual(result.returncode, 0, result.stderr)


if __name__ == "__main__":
    unittest.main()
