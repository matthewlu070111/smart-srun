#!/usr/bin/env python3
"""Check or format the school catalogue and synchronize its bundled fallback.

The default mode is read-only. Run with --write after editing
doc/school-presets.json; unknown metadata and school order are preserved.
"""

import argparse
import json
from pathlib import Path
import sys


REPO_ROOT = Path(__file__).resolve().parents[1]
MODULE_ROOT = REPO_ROOT / "root/usr/lib/smart_srun"
if str(MODULE_ROOT) not in sys.path:
    sys.path.insert(0, str(MODULE_ROOT))

DOC_PATH = Path("doc/school-presets.json")
FALLBACK_PATH = Path("root/usr/lib/smart_srun/school_presets_fallback.json")


def render_payload(payload):
    # Keep bundled and remote-cache JSON on the same presentation contract.
    from school_presets import format_preset_payload
    return format_preset_payload(payload).encode("utf-8")


def format_catalogue(repo_root, write=False):
    """Return mismatched paths, optionally writing the canonical pair."""
    repo_root = Path(repo_root)
    with (repo_root / DOC_PATH).open("r", encoding="utf-8") as handle:
        payload = json.load(handle)
    expected = {
        DOC_PATH: render_payload(payload),
        FALLBACK_PATH: render_payload(dict(payload, source="bundled fallback")),
    }
    changed = []
    for relative, contents in expected.items():
        target = repo_root / relative
        if target.exists() and target.read_bytes() == contents:
            continue
        changed.append(relative.as_posix())
        if write:
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(contents)
    return changed


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--check", action="store_true", help="Check formatting and fallback synchronization (default).")
    mode.add_argument("--write", action="store_true", help="Format the main catalogue and regenerate the fallback.")
    args = parser.parse_args(argv)
    changed = format_catalogue(REPO_ROOT, write=args.write)
    if changed:
        print(("Updated: " if args.write else "Needs formatting/synchronization: ") + ", ".join(changed))
        return 0 if args.write else 1
    print("School catalogue formatting and fallback are synchronized.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
