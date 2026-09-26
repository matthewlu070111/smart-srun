local h = dofile(arg[1] .. "/tests/lua/controller_harness.lua")(arg[1])
h.security_allowed = false
h.controller.action_config_export(); h.controller.action_config_import()
assert(#h.calls == 0 and #h.helpers == 0)
h.security_allowed = true
local exported = '{"format":"smart-srun-config","config":{"school_extra":{},"campus_accounts":[{"login":{}}],"hotspot_profiles":[]}}'
h.responses["config.export"] = {data=exported}
h.controller.action_config_export()
assert(h.calls[1].params.include_secrets == true and not h.calls[1].started)
assert(h.calls[1].params.as_json == true and h.output == exported)
assert(h.headers["Cache-Control"] == "no-store")
h.responses["config.import"] = {ok=true,expected_revision=15,campus_accounts=2,hotspot_profiles=1}
h.form = {data='{ "duplicate":1,"duplicate":2 }',check="1"}
h.controller.action_config_import()
assert(h.calls[2].params.data == h.form.data and h.calls[2].params.check_only == true)
h.form = {data="synthetic backup",expected_revision="15"}
h.controller.action_config_import()
assert(h.calls[3].params.expected_revision == 15 and not h.calls[3].params.check_only)
assert(not h.calls[3].started and #h.helpers == 0 and #h.writes == 0 and #h.commands == 0)
h.form.expected_revision="-1";h.controller.action_config_import();assert(#h.calls==3 and h.output.ok==false)
h.responses["config.export"] = {data={}}
h.controller.action_config_export();assert(h.output.ok==false)
