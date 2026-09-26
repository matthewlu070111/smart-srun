"""Release ELF inspection must distinguish ABI/endianness and fail closed."""
import importlib.util
from pathlib import Path
import struct
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))
spec = importlib.util.spec_from_file_location("verify_go_sdk", ROOT / "scripts/verify_go_sdk.py")
verify = importlib.util.module_from_spec(spec)
spec.loader.exec_module(verify)


def header(arch):
    elf_class, endian, machine = verify.ELF_TARGETS[arch]
    data = bytearray(64)
    data[:6] = b"\x7fELF" + bytes((elf_class, endian))
    data[16:20] = struct.pack(("<" if endian == 1 else ">") + "HH", 2, machine)
    return bytes(data)


class SDKVerifyTests(unittest.TestCase):
    def test_quoted_sdk_flags_allow_only_expected_inlining_setting(self):
        with tempfile.TemporaryDirectory() as temporary:
            binary = Path(temporary) / "srunnet"
            binary.write_bytes(header("mips"))
            info = '\tbuild\tCGO_ENABLED=0\n\tbuild\tGOOS=linux\n\tbuild\tGOARCH=mips\n\tbuild\tGOMIPS=softfloat\n\tbuild\t-gcflags="all=-l "\n'
            with patch.object(verify, "run", side_effect=["  LOAD  0x000000", info]):
                result = verify.inspect_binary(binary, {"goarch": "mips"}, "go", False, None, "2.0.0")
                self.assertIsNone(result["version_output"])
            with patch.object(verify, "run", side_effect=["  LOAD  0x000000", info.replace('all=-l ', 'all=-l -N')]):
                with self.assertRaisesRegex(ValueError, "gcflags"):
                    verify.inspect_binary(binary, {"goarch": "mips"}, "go", False, None, "2.0.0")

    def test_wrong_architecture_endian_and_dynamic_executable_are_rejected(self):
        for arch in verify.ELF_TARGETS:
            verify.inspect_header(header(arch), arch)
            for other in verify.ELF_TARGETS:
                if arch != other:
                    with self.subTest(arch=arch, other=other), self.assertRaises(ValueError):
                        verify.inspect_header(header(arch), other)
        data = bytearray(header("amd64"))
        data[16:18] = b"\x03\x00"
        with self.assertRaises(ValueError):
            verify.inspect_header(data, "amd64")

    def test_amd64_keeps_standard_library_inlining_and_rejects_debug_flags(self):
        with tempfile.TemporaryDirectory() as temporary:
            binary = Path(temporary) / "srunnet"
            binary.write_bytes(header("amd64"))
            base = '\tbuild\tCGO_ENABLED=0\n\tbuild\tGOOS=linux\n\tbuild\tGOARCH=amd64\n\tbuild\tGOAMD64=v1\n'
            flags = 'github.com/matthewlu070111/smart-srun/core/...=-l'
            for value in (flags, 'all=-l', flags + ' -N', ''):
                info = base + '\tbuild\t-gcflags="' + value + ' "\n'
                with self.subTest(flags=value), patch.object(verify, "run", side_effect=["  LOAD  0x000000", info]):
                    if value == flags:
                        verify.inspect_binary(binary, {"goarch": "amd64"}, "go", False, None, "2.0.0")
                    else:
                        with self.assertRaisesRegex(ValueError, "gcflags"):
                            verify.inspect_binary(binary, {"goarch": "amd64"}, "go", False, None, "2.0.0")

    def test_dynamic_loader_and_hard_float_never_pass_static_mips_check(self):
        with tempfile.TemporaryDirectory() as temporary:
            binary = Path(temporary) / "srunnet"
            binary.write_bytes(header("mips"))
            with patch.object(verify, "run", return_value="  INTERP  0x000000"):
                with self.assertRaisesRegex(ValueError, "dynamic loader"):
                    verify.inspect_binary(binary, {"goarch": "mips"}, "go", False, None, "2.0.0")
            info = "\tbuild\tCGO_ENABLED=0\n\tbuild\tGOOS=linux\n\tbuild\tGOARCH=mips\n\tbuild\tGOMIPS=hardfloat\n\tbuild\t-gcflags=all=-l\n"
            with patch.object(verify, "run", side_effect=["  LOAD  0x000000", info]):
                with self.assertRaisesRegex(ValueError, "GOMIPS"):
                    verify.inspect_binary(binary, {"goarch": "mips"}, "go", False, None, "2.0.0")


if __name__ == "__main__":
    unittest.main()
