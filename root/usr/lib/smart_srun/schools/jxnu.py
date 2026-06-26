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
        {"suffix": "cucc", "label": "中国联通"},
        {"suffix": "",     "label": "校园网"},
        {"suffix": "cmcc", "label": "中国移动"},
        {"suffix": "ctcc", "label": "中国电信"},
    )
