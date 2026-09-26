-- Stubs for the two LuCI modules the bridge pulls in through rpc.lua.
--
-- The bridge's mapping is pure; loading it must not require a router. nixio is
-- reduced to the one call rpc makes at load time, and jsonc to encode/decode
-- shapes the probe never exercises -- if a mapping function ever starts using
-- them, these stubs fail loudly rather than silently returning nothing.
package.preload["nixio"] = function()
    return {
        getpid = function() return 4242 end,
        socket = function() error("the probe must not open a socket") end,
        fork = function() error("the probe must not fork") end,
    }
end

-- rpc.lua loads nixio.util for the socket's writeall/readall. Loading it adds
-- methods and returns nothing the probe calls.
package.preload["nixio.util"] = function() return {} end

package.preload["luci.jsonc"] = function()
    return {
        stringify = function() error("the probe must not encode a frame") end,
        parse = function() error("the probe must not decode a frame") end,
    }
end
