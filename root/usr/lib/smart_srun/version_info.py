"""Version and package identity helpers for CLI and LuCI surfaces."""

from __future__ import annotations

import os
import re


PACKAGE_STATUS_FILE = "/usr/lib/opkg/status"
PACKAGE_PRIORITY = [
    "luci-app-smart-srun-bundle",
    "luci-app-smart-srun",
    "smart-srun",
]
DEFAULT_VERSION = "v0.0.0-r1"


def _read_text(path):
    try:
        with open(path, "r", encoding="utf-8") as handle:
            return handle.read()
    except OSError:
        return ""


def _find_repo_makefile():
    current = os.path.abspath(os.path.dirname(__file__))
    while True:
        candidate = os.path.join(current, "Makefile")
        text = _read_text(candidate)
        if "PKG_NAME:=luci-app-smart-srun" in text:
            return candidate
        parent = os.path.dirname(current)
        if parent == current:
            return ""
        current = parent


def normalize_version_string(raw_version):
    value = str(raw_version or "").strip()
    if not value:
        return DEFAULT_VERSION

    match = re.match(r"^v?([^-]+)-r?(\d+)$", value)
    if match:
        return "v%s-r%s" % (match.group(1), match.group(2))
    return DEFAULT_VERSION


def _package_versions_from_status(status_text):
    versions = {}
    package_name = ""
    version = ""
    for line in str(status_text or "").splitlines():
        if not line.strip():
            if package_name:
                versions[package_name] = version
            package_name = ""
            version = ""
            continue
        if line.startswith("Package:"):
            package_name = line.split(":", 1)[1].strip()
        elif line.startswith("Version:"):
            version = line.split(":", 1)[1].strip()
    if package_name:
        versions[package_name] = version
    return versions


def detect_installed_package_name(status_text=None):
    versions = _package_versions_from_status(
        _read_text(PACKAGE_STATUS_FILE) if status_text is None else status_text
    )
    for package_name in PACKAGE_PRIORITY:
        if package_name in versions:
            return package_name
    return "smart-srun"


def _makefile_version():
    text = _read_text(_find_repo_makefile())
    pkg_version = ""
    pkg_release = ""
    for line in text.splitlines():
        if line.startswith("PKG_VERSION:="):
            pkg_version = line.split(":=", 1)[1].strip()
        elif line.startswith("PKG_RELEASE:="):
            pkg_release = line.split(":=", 1)[1].strip()
    if pkg_version and pkg_release:
        return normalize_version_string("%s-%s" % (pkg_version, pkg_release))
    return DEFAULT_VERSION


def get_display_version(status_text=None, package_name=None):
    versions = _package_versions_from_status(
        _read_text(PACKAGE_STATUS_FILE) if status_text is None else status_text
    )
    selected = package_name or detect_installed_package_name(status_text=status_text)
    if selected in versions:
        return normalize_version_string(versions[selected])
    return _makefile_version()


def get_luci_badge_label(status_text=None):
    package_name = detect_installed_package_name(status_text=status_text)
    if package_name == "luci-app-smart-srun-bundle":
        return "Bundle 版"
    if package_name == "luci-app-smart-srun":
        return "标准版"
    return "CLI 版"


def get_luci_display_text(status_text=None):
    package_name = detect_installed_package_name(status_text=status_text)
    if package_name == "luci-app-smart-srun-bundle":
        label = "Bundle 版"
    elif package_name == "luci-app-smart-srun":
        label = "标准版"
    else:
        label = "CLI 版"
    return "%s %s" % (label, get_display_version(status_text, package_name))


def get_cli_version_string(status_text=None):
    package_name = detect_installed_package_name(status_text=status_text)
    return "%s %s" % (package_name, get_display_version(status_text, package_name))
