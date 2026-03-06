module("luci.controller.jxnu_srun", package.seeall)

function index()
    if not nixio.fs.access("/etc/config/jxnu_srun") then
        return
    end

    entry({"admin", "services", "jxnu_srun"}, cbi("jxnu_srun"), _("师大校园网"), 80).dependent = true
end
