#!/usr/bin/env python3

import pathlib
import sys


ROOT = pathlib.Path(__file__).resolve().parents[1]
LIB = ROOT / "root" / "usr" / "lib" / "smart_srun"
if str(LIB) not in sys.path:
    sys.path.insert(0, str(LIB))

from config import SCHOOL_EXTRA_KEY, build_school_runtime_luci_contract  # noqa: E402


def assert_equal(actual, expected, message):
    if actual != expected:
        raise AssertionError("%s: expected %r, got %r" % (message, expected, actual))


def test_bool_defaults_are_contract_normalized():
    contract = build_school_runtime_luci_contract(
        {"school": "jxnu", SCHOOL_EXTRA_KEY: {}},
        {
            "runtime_type": "runtime_class",
            "runtime_api_version": 1,
            "field_descriptors": [
                {
                    "key": "auto_bind",
                    "type": "bool",
                    "label": "Auto Bind",
                    "default": True,
                }
            ],
            "school_extra": [],
        },
    )
    assert_equal(
        contract["field_descriptors"][0]["default"],
        "1",
        "bool defaults must be normalized at the contract boundary",
    )


def test_runtime_contract_school_extra_is_normalized():
    contract = build_school_runtime_luci_contract(
        {
            "school": "jxnu",
            SCHOOL_EXTRA_KEY: {"auto_bind": "true", "retry_limit": "07"},
        },
        {
            "runtime_type": "runtime_class",
            "runtime_api_version": 1,
            "field_descriptors": [
                {"key": "auto_bind", "type": "bool", "default": False},
                {"key": "retry_limit", "type": "int", "default": 3},
            ],
            "school_extra": [],
        },
    )
    assert_equal(
        contract["school_extra"],
        {"auto_bind": "1", "retry_limit": "7"},
        "runtime contract school_extra must expose normalized values",
    )


def test_int_descriptor_without_default_does_not_crash_contract_building():
    contract = build_school_runtime_luci_contract(
        {"school": "jxnu", SCHOOL_EXTRA_KEY: {}},
        {
            "runtime_type": "runtime_class",
            "runtime_api_version": 1,
            "field_descriptors": [
                {
                    "key": "retry_limit",
                    "type": "int",
                    "label": "Retry Limit",
                }
            ],
            "school_extra": [],
        },
    )
    assert_equal(
        contract["field_descriptors"][0]["default"],
        "",
        "int descriptors without explicit default must stay empty instead of crashing",
    )


def main():
    test_bool_defaults_are_contract_normalized()
    test_runtime_contract_school_extra_is_normalized()
    test_int_descriptor_without_default_does_not_crash_contract_building()
    print("OK: school runtime LuCI behavior contracts hold")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
