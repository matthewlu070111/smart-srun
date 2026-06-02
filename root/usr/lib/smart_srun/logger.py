"""
Structured logging leaf module.

Provides:
- Hierarchical log levels: ALL < DEBUG < INFO < WARN < ERROR
- Threshold filter settable at runtime via set_log_threshold()
- Context propagation via set_log_context() / clear_log_context()
- timed() context manager for duration measurements (exposes .ms)
- log(level, event, msg, **ctx) and legacy append_log(line)
- 512 KiB rotating file log at /var/log/smart_srun.log

Must remain free of dependencies on any other smart_srun module so that
network/config/wireless/etc. can import it without cycles.
"""

import os
import time
from datetime import datetime, timedelta, timezone

BEIJING_TZ = timezone(timedelta(hours=8))
LOG_FILE = "/var/log/smart_srun.log"
LOG_MAX_BYTES = 512 * 1024

LOG_LEVEL_WEIGHTS = {
    "ALL": 0,
    "DEBUG": 10,
    "INFO": 20,
    "WARN": 30,
    "ERROR": 40,
}
LOG_LEVEL_NAMES = ("ALL", "DEBUG", "INFO", "WARN", "ERROR")
DEFAULT_LOG_LEVEL = "INFO"

_EMIT_WEIGHTS = {k: v for k, v in LOG_LEVEL_WEIGHTS.items() if k != "ALL"}

_threshold = LOG_LEVEL_WEIGHTS[DEFAULT_LOG_LEVEL]
_context = {}

_REDACTED_VALUE = "***"
_SENSITIVE_KEY_PARTS = (
    "password",
    "passwd",
    "secret",
    "token",
    "chksum",
    "checksum",
    "hmd5",
    "credential",
    "authorization",
    "cookie",
    "session",
    "private_key",
    "campus_key",
    "hotspot_key",
    "wifi_key",
    "psk",
)


def normalize_level(level):
    name = str(level or "").strip().upper()
    if name in LOG_LEVEL_WEIGHTS:
        return name
    return DEFAULT_LOG_LEVEL


def set_log_threshold(level):
    """Set minimum level to emit. Unknown levels fall back to INFO."""
    global _threshold
    name = normalize_level(level)
    _threshold = LOG_LEVEL_WEIGHTS[name]
    return name


def get_log_threshold():
    for name, weight in LOG_LEVEL_WEIGHTS.items():
        if weight == _threshold:
            return name
    return DEFAULT_LOG_LEVEL


def set_log_context(**kv):
    """Merge key/value pairs into the module-wide log context."""
    for k, v in kv.items():
        _context[str(k)] = v


def clear_log_context(*keys):
    """Drop specific keys, or all keys when called with no arguments."""
    if not keys:
        _context.clear()
        return
    for k in keys:
        _context.pop(str(k), None)


class timed(object):
    """Context manager measuring wall-clock duration in milliseconds.

    Usage:
        with timed() as t:
            do_work()
        log("INFO", "work_done", duration_ms=t.ms)
    """

    def __init__(self):
        self._start = 0.0
        self._end = None
        self.ms = 0

    def __enter__(self):
        self._start = time.monotonic()
        return self

    def __exit__(self, exc_type, exc, tb):
        self._end = time.monotonic()
        self.ms = int((self._end - self._start) * 1000)
        return False


def _format_text(value):
    sv = str(value)
    sv = sv.replace("\\", "\\\\")
    sv = sv.replace("\r", "\\r").replace("\n", "\\n").replace("\t", "\\t")
    return sv


def _is_sensitive_key(key):
    name = str(key or "").strip().lower()
    return any(part in name for part in _SENSITIVE_KEY_PARTS)


def _format_value(value, key=None):
    sv = _REDACTED_VALUE if _is_sensitive_key(key) else _format_text(value)
    if " " in sv or not sv or '"' in sv:
        sv = '"%s"' % sv.replace('"', '\\"')
    return sv


def log(level, event, msg="", **ctx):
    """Emit a structured log line if level meets the current threshold.

    Format: [YYYY-MM-DD HH:MM:SS] LEVEL event k=v ... | msg
    """
    name = normalize_level(level)
    weight = _EMIT_WEIGHTS.get(name)
    if weight is None or weight < _threshold:
        return

    timestamp = datetime.now(BEIJING_TZ).strftime("%Y-%m-%d %H:%M:%S")
    parts = [name, str(event)]

    if _context:
        for k, v in _context.items():
            if k in ctx:
                continue
            parts.append("%s=%s" % (k, _format_value(v, k)))
    for k, v in ctx.items():
        parts.append("%s=%s" % (k, _format_value(v, k)))

    if msg:
        parts.append("| %s" % _format_text(msg))

    _write_log("[%s] %s" % (timestamp, " ".join(parts)))


def append_log(line):
    """Legacy compat wrapper. New code should use log()."""
    _write_log(
        "[%s] %s"
        % (
            datetime.now(BEIJING_TZ).strftime("%Y-%m-%d %H:%M:%S"),
            _format_text(str(line).strip()),
        )
    )


def _write_log(log_line):
    """Write a pre-formatted log line to stdout and log file with rotation."""
    print(log_line, flush=True)
    try:
        if os.path.exists(LOG_FILE) and os.path.getsize(LOG_FILE) > LOG_MAX_BYTES:
            _rotate_log_tail()

        with open(LOG_FILE, "a", encoding="utf-8") as af:
            af.write(log_line + "\n")
    except OSError:
        pass


def _rotate_log_tail():
    keep_bytes = LOG_MAX_BYTES // 2
    with open(LOG_FILE, "rb") as rf:
        rf.seek(0, os.SEEK_END)
        size = rf.tell()
        rf.seek(max(0, size - keep_bytes), os.SEEK_SET)
        if size > keep_bytes:
            rf.readline()
        keep = rf.read()

    with open(LOG_FILE, "wb") as wf:
        wf.write(keep)
