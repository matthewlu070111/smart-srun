local root = assert(arg[1])
package.path = root .. '/root/usr/lib/lua/?.lua;' .. package.path
local status = ''
package.preload['nixio.fs'] = function() return { readfile = function(path)
    return path == '/usr/lib/opkg/status' and status or nil
end } end
package.preload['nixio'] = function() return {} end
package.preload['luci.jsonc'] = function() return {parse = function() return {} end} end
local schema = require 'luci.smart_srun.schema'
for native, expected in pairs({ ['2.0.0~rc39-r1'] = 'v2.0.0rc39', ['2.0.0_rc2-r1'] = 'v2.0.0rc2',
    ['2.0.0-r1'] = 'v2.0.0', ['1.6.0-1'] = 'v1.6.0' }) do
    status = 'Package: luci-app-smart-srun-bundle\nVersion: ' .. native .. '\nStatus: install ok installed\n\n'
    assert(schema.installed_package_display_text() == 'Bundle 版 ' .. expected, native)
end
print('native version display: passed')
