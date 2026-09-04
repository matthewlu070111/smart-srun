#!/usr/bin/env python3
"""Check the school catalogue and synchronize its bundled fallback.

The default mode checks JSON content without requiring matching formatting.
With --write, copy doc/school-presets.json verbatim to the fallback, replacing
only its top-level source string. The main catalogue is never written.
"""

import argparse
import json
from pathlib import Path


REPO_ROOT = Path(__file__).resolve().parents[1]
DOC_PATH = Path("doc/school-presets.json")
FALLBACK_PATH = Path("root/usr/lib/smart_srun/school_presets_fallback.json")


def _skip_space(text, offset):
    while offset < len(text) and text[offset] in " \t\r\n":
        offset += 1
    return offset


def _fallback_contents(contents):
    """Return parsed main data and its text with only the source value replaced."""
    text = contents.decode("utf-8")
    decoder = json.JSONDecoder()
    payload = decoder.decode(text)
    if not isinstance(payload, dict) or not isinstance(payload.get("source"), str):
        raise ValueError("Main catalogue must be an object with a source string")

    source_span = None
    offset = _skip_space(text, 0) + 1  # The validated top-level opening brace.
    while True:
        offset = _skip_space(text, offset)
        if text[offset] == "}":
            break
        key, offset = decoder.raw_decode(text, offset)
        offset = _skip_space(text, offset) + 1  # The validated colon.
        start = _skip_space(text, offset)
        _, offset = decoder.raw_decode(text, start)
        if key == "source":
            if source_span is not None:
                raise ValueError("Main catalogue has duplicate top-level source keys")
            source_span = (start, offset)
        offset = _skip_space(text, offset)
        if text[offset] == ",":
            offset += 1

    start, end = source_span
    replacement = json.dumps("bundled fallback")
    return payload, (text[:start] + replacement + text[end:]).encode("utf-8")


def sync_catalogue(repo_root, write=False):
    """Return mismatched paths; --write regenerates only the fallback text."""
    repo_root = Path(repo_root)
    payload, expected_text = _fallback_contents((repo_root / DOC_PATH).read_bytes())
    target = repo_root / FALLBACK_PATH
    current = target.read_bytes() if target.exists() else None
    if write:
        if current == expected_text:
            return []
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(expected_text)
    elif current is not None:
        try:
            fallback = json.loads(current)
        except (UnicodeError, ValueError):
            pass
        else:
            if fallback == dict(payload, source="bundled fallback"):
                return []
    return [FALLBACK_PATH.as_posix()]


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--check", action="store_true", help="Check JSON content and fallback synchronization (default).")
    mode.add_argument("--write", action="store_true", help="Regenerate fallback from the unchanged main catalogue text.")
    args = parser.parse_args(argv)
    try:
        changed = sync_catalogue(REPO_ROOT, write=args.write)
    except (OSError, ValueError) as exc:
        print("Cannot synchronize school catalogue: " + str(exc))
        return 1
    if changed:
        print(("Updated: " if args.write else "Needs synchronization: ") + ", ".join(changed))
        return 0 if args.write else 1
    print("School catalogue and fallback content are synchronized.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
