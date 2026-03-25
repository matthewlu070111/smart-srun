"""
SchoolProfile 基类 -- 默认 SRun 认证协议实现。

其他学校继承此类并覆盖需要修改的部分（通常只需改 ALPHA 和运营商列表）。
Profile 只做数据变换，不做 I/O。HTTP 请求由 srun_auth.py 负责。
"""

import sys
import os
import time

# 确保父目录在 sys.path 中，以便 import crypto
_parent_dir = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
if _parent_dir not in sys.path:
    sys.path.insert(0, _parent_dir)

import crypto


class SchoolProfile:
    # -- 元数据 --
    NAME = "默认 SRun"
    SHORT_NAME = "default"
    DESCRIPTION = "深澜 SRun 4000 系列认证（默认实现）"
    CONTRIBUTORS = ()

    # -- 运营商 --
    OPERATORS = ()
    NO_SUFFIX_OPERATORS = ()

    # -- 协议参数 --
    ALPHA = crypto.ALPHA
    DEFAULT_BASE_URL = ""
    DEFAULT_AC_ID = "1"
    DEFAULT_N = "200"
    DEFAULT_TYPE = "1"
    DEFAULT_ENC = "srun_bx1"

    # -- API 路径 --
    API_CHALLENGE = "/cgi-bin/get_challenge"
    API_PORTAL = "/cgi-bin/srun_portal"
    API_RAD_USER_INFO = "/cgi-bin/rad_user_info"
    API_RAD_USER_DM = "/cgi-bin/rad_user_dm"

    # -----------------------------------------------------------------
    # 用户名构建
    # -----------------------------------------------------------------

    def build_username(self, user_id, operator):
        if operator in self.NO_SUFFIX_OPERATORS:
            return user_id
        return user_id + "@" + operator

    def build_urls(self, base_url):
        return {
            "init_url": base_url,
            "get_challenge_api": base_url + self.API_CHALLENGE,
            "srun_portal_api": base_url + self.API_PORTAL,
            "rad_user_info_api": base_url + self.API_RAD_USER_INFO,
            "rad_user_dm_api": base_url + self.API_RAD_USER_DM,
        }

    # -----------------------------------------------------------------
    # 加密方法（大多数学校只需改 ALPHA 类属性）
    # -----------------------------------------------------------------

    def get_base64(self, value):
        return crypto.get_base64(value, self.ALPHA)

    def get_xencode(self, msg, key):
        return crypto.get_xencode(msg, key)

    def get_md5(self, password, token):
        return crypto.get_md5(password, token)

    def get_sha1(self, value):
        return crypto.get_sha1(value)

    def get_info(self, username, password, ip, ac_id, enc):
        return crypto.get_info(username, password, ip, ac_id, enc)

    # -----------------------------------------------------------------
    # 复合加密
    # -----------------------------------------------------------------

    def do_complex_work(self, cfg, ip, token):
        i_value = self.get_info(
            cfg["username"], cfg["password"], ip, cfg["ac_id"], cfg["enc"]
        )
        i_value = "{SRBX1}" + self.get_base64(self.get_xencode(i_value, token))
        hmd5 = self.get_md5(cfg["password"], token)
        chkstr = crypto.get_chksum(
            token,
            cfg["username"],
            hmd5,
            cfg["ac_id"],
            ip,
            cfg["n"],
            cfg["type"],
            i_value,
        )
        chksum = self.get_sha1(chkstr)
        return i_value, hmd5, chksum

    # -----------------------------------------------------------------
    # 请求构建 / 响应解析（纯数据变换，不做 HTTP）
    # -----------------------------------------------------------------

    def build_login_params(self, cfg, ip, i_value, hmd5, chksum):
        now = int(time.time() * 1000)
        return {
            "callback": "jQuery11240645308969735664_" + str(now),
            "action": "login",
            "username": cfg["username"],
            "password": "{MD5}" + hmd5,
            "ac_id": cfg["ac_id"],
            "ip": ip,
            "chksum": chksum,
            "info": i_value,
            "n": cfg["n"],
            "type": cfg["type"],
            "os": "openwrt",
            "name": "openwrt",
            "double_stack": "0",
            "_": now,
        }

    def parse_login_response(self, data):
        error = str(data.get("error", "")).lower()
        result = str(data.get("res", "")).lower()
        success = error == "ok" or result == "ok"
        message = data.get("error_msg") or data.get("error") or "unknown response"
        return success, str(message)

    def build_logout_params(self, cfg, ip):
        now = int(time.time())
        username = self._get_logout_username(cfg)
        unbind = "1"
        return {
            "callback": "jQuery11240645308969735664_" + str(now),
            "time": str(now),
            "unbind": unbind,
            "ip": ip,
            "username": username,
            "sign": crypto.get_sha1(
                str(now) + username + ip + unbind + str(now)
            ),
        }

    def parse_logout_response(self, data):
        error = str(data.get("error", "")).lower()
        result = str(data.get("res", "")).lower()
        success = error == "ok" or result == "ok"
        message = (
            data.get("error_msg")
            or data.get("error")
            or data.get("res")
            or "unknown response"
        )
        return success, str(message)

    def build_online_query_params(self):
        now = int(time.time() * 1000)
        return {
            "callback": "jQuery112406118340540763985_" + str(now),
            "_": now,
        }

    def parse_online_status(self, data, expected_username):
        if str(data.get("error", "")).lower() != "ok":
            msg = data.get("error_msg") or data.get("error") or "unknown response"
            return False, "", msg
        online_name = str(data.get("user_name", "")).strip()
        expected_main = expected_username.split("@", 1)[0]
        if online_name and online_name == expected_main:
            return True, online_name, "在线"
        if online_name:
            return True, online_name, "在线账号: %s" % online_name
        return False, "", "离线"

    def _get_logout_username(self, cfg):
        user_id = str(cfg.get("user_id", "")).strip()
        if user_id:
            return user_id
        return str(cfg.get("username", "")).split("@", 1)[0].strip()
