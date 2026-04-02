"""
SRun 认证协议加密工具 -- 纯函数，零依赖，零 I/O。

提供自定义 Base64 编解码、HMAC-MD5、SHA1、BX1 异或编码等 SRun 协议所需的加密原语。
SchoolProfile 基类和 srun_auth 模块通过调用这些函数完成认证参数计算。
"""

import hashlib
import hmac
import json
import math

# 默认 SRun Base64 字母表（江西师范大学），其他学校可在 SchoolProfile 中覆盖
ALPHA = "LVoJPiCN2R8G90yg+hmFHuacZ1OWMnrsSTXkYpUq/3dlbfKwv6xztjI7DeBE45QA"
PAD_CHAR = "="


def _getbyte(value, idx):
    ch = ord(value[idx])
    if ch > 255:
        raise ValueError("INVALID_CHARACTER_ERR")
    return ch


def get_base64(value, alpha=None):
    if alpha is None:
        alpha = ALPHA
    b10 = 0
    output = []
    imax = len(value) - len(value) % 3
    if len(value) == 0:
        return value

    for idx in range(0, imax, 3):
        b10 = (
            (_getbyte(value, idx) << 16)
            | (_getbyte(value, idx + 1) << 8)
            | _getbyte(value, idx + 2)
        )
        output.append(alpha[(b10 >> 18)])
        output.append(alpha[((b10 >> 12) & 63)])
        output.append(alpha[((b10 >> 6) & 63)])
        output.append(alpha[(b10 & 63)])

    idx = imax
    remain = len(value) - imax
    if remain == 0:
        return "".join(output)
    if remain == 1:
        b10 = _getbyte(value, idx) << 16
        output.append(
            alpha[(b10 >> 18)] + alpha[((b10 >> 12) & 63)] + PAD_CHAR + PAD_CHAR
        )
    else:
        b10 = (_getbyte(value, idx) << 16) | (_getbyte(value, idx + 1) << 8)
        output.append(
            alpha[(b10 >> 18)]
            + alpha[((b10 >> 12) & 63)]
            + alpha[((b10 >> 6) & 63)]
            + PAD_CHAR
        )
    return "".join(output)


def get_md5(password, token):
    return hmac.new(token.encode(), password.encode(), hashlib.md5).hexdigest()


def get_sha1(value):
    return hashlib.sha1(value.encode()).hexdigest()


def ordat(msg, idx):
    if len(msg) > idx:
        return ord(msg[idx])
    return 0


def sencode(msg, key):
    length = len(msg)
    pwd = []
    for i in range(0, length, 4):
        pwd.append(
            ordat(msg, i)
            | ordat(msg, i + 1) << 8
            | ordat(msg, i + 2) << 16
            | ordat(msg, i + 3) << 24
        )
    if key:
        pwd.append(length)
    return pwd


def lencode(msg, key):
    length = len(msg)
    ll = (length - 1) << 2
    if key:
        m_val = msg[length - 1]
        if m_val < ll - 3 or m_val > ll:
            return None
        ll = m_val
    for i in range(0, length):
        msg[i] = (
            chr(msg[i] & 0xFF)
            + chr(msg[i] >> 8 & 0xFF)
            + chr(msg[i] >> 16 & 0xFF)
            + chr(msg[i] >> 24 & 0xFF)
        )
    if key:
        return "".join(msg)[0:ll]
    return "".join(msg)


def get_xencode(msg, key):
    """XXTEA (Corrected Block TEA) 加密，来自 SRun 前端 JS 混淆代码的 Python 翻译。"""
    if msg == "":
        return ""
    pwd = sencode(msg, True)
    pwdk = sencode(key, False)
    if len(pwdk) < 4:
        pwdk = pwdk + [0] * (4 - len(pwdk))

    # TEA 常量 -- SRun JS 混淆器将 0x9E3779B9 拆为 OR 运算，0xFFFFFFFF 同理
    DELTA = 0x86014019 | 0x183639A0  # = 0x9E3779B9 (golden ratio)
    MASK_32 = 0x8CE0D9BF | 0x731F2640  # = 0xFFFFFFFF (32-bit mask)

    n_val = len(pwd) - 1
    z_val = pwd[n_val]
    q_val = math.floor(6 + 52 / (n_val + 1))
    d_val = 0

    while 0 < q_val:
        d_val = (d_val + DELTA) & MASK_32
        e_val = d_val >> 2 & 3
        p_val = 0
        while p_val < n_val:
            y_val = pwd[p_val + 1]
            m_val = z_val >> 5 ^ y_val << 2
            m_val = m_val + ((y_val >> 3 ^ z_val << 4) ^ (d_val ^ y_val))
            m_val = m_val + (pwdk[(p_val & 3) ^ e_val] ^ z_val)
            pwd[p_val] = (pwd[p_val] + m_val) & MASK_32
            z_val = pwd[p_val]
            p_val = p_val + 1
        y_val = pwd[0]
        m_val = z_val >> 5 ^ y_val << 2
        m_val = m_val + ((y_val >> 3 ^ z_val << 4) ^ (d_val ^ y_val))
        m_val = m_val + (pwdk[(p_val & 3) ^ e_val] ^ z_val)
        pwd[n_val] = (pwd[n_val] + m_val) & MASK_32
        z_val = pwd[n_val]
        q_val = q_val - 1
    return lencode(pwd, False)


def get_info(username, password, ip, ac_id, enc):
    info_temp = {
        "username": username,
        "password": password,
        "ip": ip,
        "acid": ac_id,
        "enc_ver": enc,
    }
    return json.dumps(info_temp, separators=(",", ":"))


def get_chksum(token, username, hmd5, ac_id, ip, n_value, type_value, i_value):
    chkstr = token + username
    chkstr += token + hmd5
    chkstr += token + ac_id
    chkstr += token + ip
    chkstr += token + n_value
    chkstr += token + type_value
    chkstr += token + i_value
    return chkstr
