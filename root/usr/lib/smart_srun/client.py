#!/usr/bin/python3
"""
SMART SRun 校园网客户端入口。

所有业务逻辑已拆分至独立模块：
  crypto.py      - 加密算法
  network.py     - HTTP 客户端 / IP 工具
  config.py      - 配置管理 / 日志 / 策略
  wireless.py    - UCI 无线管理 / SSID 切换
  srun_auth.py   - SRun 认证 API
  orchestrator.py - 登录/登出编排 / 重试
  daemon.py      - 守护循环 / CLI 分发
  schools/       - 学校 Profile 插件

本文件仅作为启动入口，保持路径兼容：
  python3 -B /usr/lib/smart_srun/client.py daemon
"""

import os
import sys

# 确保模块目录在 sys.path 中
_here = os.path.dirname(os.path.abspath(__file__))
if _here not in sys.path:
    sys.path.insert(0, _here)

from daemon import main

if __name__ == "__main__":
    main()
