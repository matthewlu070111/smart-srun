"""
江西师范大学 -- 深澜 SRun 4000 系列认证（瑶湖/青山湖校区）
"""

from _base import SchoolProfile


class Profile(SchoolProfile):
    NAME = "默认配置"
    SHORT_NAME = "jxnu"
    DESCRIPTION = "江西师范大学，南昌大学"
    CONTRIBUTORS = ("@matthewlu070111", "@guiguisocute")

    ALPHA = "LVoJPiCN2R8G90yg+hmFHuacZ1OWMnrsSTXkYpUq/3dlbfKwv6xztjI7DeBE45QA"
    DEFAULT_BASE_URL = "http://172.17.1.2"
    DEFAULT_AC_ID = "1"

    OPERATORS = (
        {"id": "cucc", "label": "中国联通", "verified": True},
        {"id": "xn",   "label": "校园网",   "verified": True},
        {"id": "cmcc", "label": "中国移动", "verified": True},
        {"id": "ctcc", "label": "中国电信", "verified": False},
    )
    NO_SUFFIX_OPERATORS = ("xn",)
