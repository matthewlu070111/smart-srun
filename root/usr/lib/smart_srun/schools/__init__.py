"""
School runtime registry and metadata discovery.
"""

import importlib.util
import os
import sys

from ._base import SchoolProfile


_SCHOOL_ENTRIES = {}
_LOADED = False


def _copy_list(value):
    if not value:
        return []
    return list(value)


def _normalize_school_metadata(metadata):
    item = dict(metadata or {})
    short_name = str(item.get("short_name", "")).strip()
    if not short_name:
        raise ValueError("school metadata requires short_name")
    item["short_name"] = short_name
    item["name"] = str(item.get("name") or short_name)
    item["description"] = str(item.get("description") or "")
    item["contributors"] = _copy_list(item.get("contributors"))
    item["operators"] = _copy_list(item.get("operators"))
    item["no_suffix_operators"] = _copy_list(item.get("no_suffix_operators"))
    if "capabilities" in item:
        item["capabilities"] = _copy_list(item.get("capabilities"))
    return item


def _metadata_from_profile_class(profile_class):
    return _normalize_school_metadata(
        {
            "short_name": getattr(profile_class, "SHORT_NAME", ""),
            "name": getattr(profile_class, "NAME", ""),
            "description": getattr(profile_class, "DESCRIPTION", ""),
            "contributors": getattr(profile_class, "CONTRIBUTORS", ()),
            "operators": getattr(profile_class, "OPERATORS", ()),
            "no_suffix_operators": getattr(profile_class, "NO_SUFFIX_OPERATORS", ()),
        }
    )


def _load_module(mod_name, filepath):
    spec = importlib.util.spec_from_file_location(
        "schools." + mod_name,
        filepath,
        submodule_search_locations=[],
    )
    if spec is None or spec.loader is None:
        raise ImportError("cannot load school module: %s" % filepath)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def _build_school_entry(mod_name, filepath, module):
    has_builder = callable(getattr(module, "build_runtime", None))
    has_runtime = getattr(module, "Runtime", None) is not None
    profile_class = getattr(module, "Profile", None)

    if has_builder or has_runtime:
        metadata = getattr(module, "SCHOOL_METADATA", None)
        if not isinstance(metadata, dict):
            raise ValueError("full runtime modules must define SCHOOL_METADATA")
        runtime_type = "build_runtime" if has_builder else "runtime_class"
        metadata = _normalize_school_metadata(metadata)
    elif profile_class is not None:
        runtime_type = "legacy_profile"
        metadata = _metadata_from_profile_class(profile_class)
    else:
        return None

    return {
        "short_name": metadata["short_name"],
        "metadata": metadata,
        "module": module,
        "runtime_type": runtime_type,
        "source_file": filepath,
        "module_name": mod_name,
    }


def _discover():
    global _LOADED
    if _LOADED:
        return
    pkg_dir = os.path.dirname(os.path.abspath(__file__))
    if pkg_dir not in sys.path:
        sys.path.insert(0, pkg_dir)
    for fname in sorted(os.listdir(pkg_dir)):
        if fname.startswith("_") or not fname.endswith(".py"):
            continue
        mod_name = fname[:-3]
        filepath = os.path.join(pkg_dir, fname)
        try:
            module = _load_module(mod_name, filepath)
            entry = _build_school_entry(mod_name, filepath, module)
            if entry:
                _SCHOOL_ENTRIES[entry["short_name"]] = entry
        except Exception as exc:
            print("WARN: schools: skip %s: %s" % (fname, exc), file=sys.stderr)
    _LOADED = True


def get_school_entry(short_name):
    _discover()
    return _SCHOOL_ENTRIES.get(short_name)


def get_school_metadata(short_name):
    entry = get_school_entry(short_name)
    if not entry:
        return None
    return dict(entry["metadata"])


def get_default_school_metadata():
    return _metadata_from_profile_class(SchoolProfile)


def get_profile(short_name):
    try:
        import school_runtime

        return school_runtime.resolve_runtime({"school": short_name})
    except LookupError:
        return None


def list_schools():
    _discover()
    items = []
    for entry in sorted(_SCHOOL_ENTRIES.values(), key=lambda item: item["short_name"]):
        items.append(dict(entry["metadata"]))
    return items


def get_default_profile():
    import school_runtime

    _discover()
    if "jxnu" in _SCHOOL_ENTRIES:
        return school_runtime.resolve_runtime({"school": "jxnu"})
    if _SCHOOL_ENTRIES:
        return school_runtime.resolve_runtime(
            {"school": next(iter(_SCHOOL_ENTRIES.keys()))}
        )
    return school_runtime.DefaultRuntime()
