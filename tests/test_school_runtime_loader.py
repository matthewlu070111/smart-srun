import importlib
import sys
import textwrap
import unittest

from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]
LIB_DIR = REPO_ROOT / "root" / "usr" / "lib" / "smart_srun"
SCHOOLS_DIR = LIB_DIR / "schools"

if str(LIB_DIR) not in sys.path:
    sys.path.insert(0, str(LIB_DIR))


def load_school_runtime_module(test_case):
    try:
        return importlib.import_module("school_runtime")
    except ImportError:
        test_case.fail("school_runtime module missing")


def load_srun_auth_module(test_case):
    try:
        return importlib.import_module("srun_auth")
    except ImportError:
        test_case.fail("srun_auth module missing")


class TemporarySchoolModule:
    def __init__(self, name, source):
        self.name = name
        self.source = textwrap.dedent(source).lstrip()
        self.path = SCHOOLS_DIR / (name + ".py")

    def __enter__(self):
        self.path.write_text(self.source, encoding="utf-8", newline="\n")
        return self.path

    def __exit__(self, exc_type, exc, tb):
        if self.path.exists():
            self.path.unlink()
        pycache = SCHOOLS_DIR / "__pycache__"
        if pycache.exists():
            for item in pycache.glob(self.name + ".*.pyc"):
                item.unlink()
        sys.modules.pop("schools." + self.name, None)


class SchoolRuntimeLoaderTests(unittest.TestCase):
    def tearDown(self):
        for name in ["schools", "school_runtime", "srun_auth"]:
            if name in sys.modules:
                importlib.reload(sys.modules[name])

    def test_list_schools_keeps_legacy_metadata_shape(self):
        schools = importlib.import_module("schools")

        jxnu = None
        for item in schools.list_schools():
            if item["short_name"] == "jxnu":
                jxnu = item
                break

        self.assertIsNotNone(jxnu)
        self.assertEqual(jxnu["name"], "默认配置")
        self.assertIn("description", jxnu)
        self.assertIn("contributors", jxnu)
        self.assertIn("operators", jxnu)
        self.assertIn("no_suffix_operators", jxnu)
        self.assertEqual(jxnu["no_suffix_operators"], ["xn"])

    def test_list_schools_uses_school_metadata_without_building_runtime(self):
        with TemporarySchoolModule(
            "zz_runtime_metadata_only",
            """
            SCHOOL_METADATA = {
                "short_name": "runtime-meta",
                "name": "Runtime Metadata",
                "description": "metadata only",
                "contributors": ["@loader"],
                "operators": [{"id": "xn", "label": "Campus", "verified": True}],
                "no_suffix_operators": ["xn"],
                "capabilities": ["healthcheck"],
            }

            def build_runtime(core_api, cfg):
                raise RuntimeError("list_schools must not build runtime")
            """,
        ):
            schools = importlib.reload(importlib.import_module("schools"))
            runtime_meta = None
            for item in schools.list_schools():
                if item["short_name"] == "runtime-meta":
                    runtime_meta = item
                    break

        self.assertIsNotNone(runtime_meta)
        self.assertEqual(runtime_meta["name"], "Runtime Metadata")
        self.assertEqual(runtime_meta["no_suffix_operators"], ["xn"])
        self.assertEqual(runtime_meta["capabilities"], ["healthcheck"])

    def test_resolve_runtime_prefers_build_runtime_then_runtime_then_profile(self):
        school_runtime = load_school_runtime_module(self)

        with (
            TemporarySchoolModule(
                "zz_runtime_builder",
                """
            from school_runtime import RUNTIME_API_VERSION

            SCHOOL_METADATA = {
                "short_name": "builder-school",
                "name": "Builder School",
                "description": "builder wins",
                "contributors": [],
                "operators": [],
                "no_suffix_operators": [],
                "capabilities": ["builder"],
            }

            class Runtime(object):
                def __init__(self, core_api, cfg):
                    self.selected = "runtime-class"

            class Profile(object):
                SHORT_NAME = "builder-school"

            def build_runtime(core_api, cfg):
                runtime = type("BuilderRuntime", (), {})()
                runtime.selected = "builder"
                runtime.runtime_api_version = RUNTIME_API_VERSION
                runtime.declared_capabilities = ("builder",)
                return runtime
            """,
            ),
            TemporarySchoolModule(
                "zz_runtime_class",
                """
            from school_runtime import RUNTIME_API_VERSION

            SCHOOL_METADATA = {
                "short_name": "runtime-class-school",
                "name": "Runtime Class School",
                "description": "runtime class wins",
                "contributors": [],
                "operators": [],
                "no_suffix_operators": [],
            }

            class Runtime(object):
                def __init__(self, core_api, cfg):
                    self.selected = "runtime-class"
                    self.runtime_api_version = RUNTIME_API_VERSION
                    self.declared_capabilities = ()

            class Profile(object):
                SHORT_NAME = "runtime-class-school"
            """,
            ),
            TemporarySchoolModule(
                "zz_legacy_profile_only",
                """
            class Profile(object):
                SHORT_NAME = "legacy-only"
                NAME = "Legacy Only"
                DESCRIPTION = "profile fallback"
                CONTRIBUTORS = ()
                OPERATORS = ()
                NO_SUFFIX_OPERATORS = ()
            """,
            ),
        ):
            school_runtime = importlib.reload(school_runtime)

            builder = school_runtime.resolve_runtime({"school": "builder-school"})
            runtime_class = school_runtime.resolve_runtime(
                {"school": "runtime-class-school"}
            )
            legacy = school_runtime.resolve_runtime({"school": "legacy-only"})

        self.assertEqual(builder.selected, "builder")
        self.assertEqual(runtime_class.selected, "runtime-class")
        self.assertIsInstance(legacy, school_runtime.LegacyProfileRuntimeAdapter)
        self.assertEqual(legacy.SHORT_NAME, "legacy-only")

    def test_schools_get_profile_keeps_legacy_metadata_fields_on_runtime_objects(self):
        with TemporarySchoolModule(
            "zz_runtime_metadata_bridge",
            """
            from school_runtime import RUNTIME_API_VERSION

            SCHOOL_METADATA = {
                "short_name": "runtime-bridge",
                "name": "Runtime Bridge",
                "description": "metadata bridge",
                "contributors": ["@bridge"],
                "operators": [{"id": "cucc", "label": "CUCC", "verified": True}],
                "no_suffix_operators": ["xn"],
            }

            class Runtime(object):
                def __init__(self, core_api, cfg):
                    self.runtime_api_version = RUNTIME_API_VERSION
                    self.declared_capabilities = ()
            """,
        ):
            schools = importlib.reload(importlib.import_module("schools"))
            runtime = schools.get_profile("runtime-bridge")

        self.assertEqual(runtime.SHORT_NAME, "runtime-bridge")
        self.assertEqual(runtime.NAME, "Runtime Bridge")
        self.assertEqual(runtime.DESCRIPTION, "metadata bridge")
        self.assertEqual(
            runtime.OPERATORS, ({"id": "cucc", "label": "CUCC", "verified": True},)
        )
        self.assertEqual(runtime.NO_SUFFIX_OPERATORS, ("xn",))

    def test_resolve_runtime_rejects_unknown_school_but_allows_default_paths(self):
        school_runtime = load_school_runtime_module(self)

        with self.assertRaises(LookupError):
            school_runtime.resolve_runtime({"school": "missing-school"})

        default_runtime = school_runtime.resolve_runtime({})
        explicit_default = school_runtime.resolve_runtime({"school": "default"})

        self.assertIsInstance(default_runtime, school_runtime.DefaultRuntime)
        self.assertIsInstance(explicit_default, school_runtime.DefaultRuntime)

    def test_srun_auth_get_profile_rejects_invalid_explicit_school(self):
        srun_auth = load_srun_auth_module(self)

        with self.assertRaises(LookupError):
            srun_auth.get_profile({"school": "missing-school"})

    def test_get_default_profile_prefers_jxnu_when_available(self):
        schools = importlib.import_module("schools")
        default_profile = schools.get_default_profile()

        self.assertEqual(default_profile.SHORT_NAME, "jxnu")
        self.assertEqual(default_profile.NAME, "默认配置")

    def test_inspect_runtime_exposes_runtime_contract_details(self):
        school_runtime = load_school_runtime_module(self)

        with TemporarySchoolModule(
            "zz_runtime_inspectable",
            """
            from school_runtime import RUNTIME_API_VERSION

            SCHOOL_METADATA = {
                "short_name": "inspectable-school",
                "name": "Inspectable School",
                "description": "inspect me",
                "contributors": ["@inspect"],
                "operators": [{"id": "cucc", "label": "CUCC", "verified": True}],
                "no_suffix_operators": ["xn"],
                "capabilities": ["inspect", "status"],
            }

            def build_runtime(core_api, cfg):
                runtime = type("InspectableRuntime", (), {})()
                runtime.runtime_api_version = RUNTIME_API_VERSION
                runtime.declared_capabilities = ("inspect", "status")
                return runtime
            """,
        ):
            school_runtime = importlib.reload(school_runtime)
            info = school_runtime.inspect_runtime({"school": "inspectable-school"})

        self.assertEqual(info["short_name"], "inspectable-school")
        self.assertEqual(info["runtime_type"], "build_runtime")
        self.assertEqual(
            info["runtime_api_version"], school_runtime.RUNTIME_API_VERSION
        )
        self.assertTrue(info["source_file"].endswith("zz_runtime_inspectable.py"))
        self.assertEqual(info["declared_capabilities"], ["inspect", "status"])

    def test_build_app_context_contains_runtime_and_core_api(self):
        school_runtime = load_school_runtime_module(self)
        runtime = school_runtime.resolve_runtime({})
        context = school_runtime.build_app_context(
            {"school": "default"}, runtime=runtime
        )

        self.assertIs(context["runtime"], runtime)
        self.assertIn("core_api", context)
        self.assertEqual(
            context["runtime_api_version"], school_runtime.RUNTIME_API_VERSION
        )


if __name__ == "__main__":
    unittest.main()
