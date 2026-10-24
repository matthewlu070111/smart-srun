#!/usr/bin/env python3
"""Print local guidance and suggested checks selected by changed paths."""
import argparse
import fnmatch
import json
import os
from pathlib import Path, PurePosixPath
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[1]
CONFIG = ".github/review-context.json"


def git(root, *args):
    result = subprocess.run(["git", "-C", str(root), *args], capture_output=True)
    if result.returncode:
        raise ValueError(result.stderr.decode(errors="replace").strip() or "Git command failed")
    return result.stdout


def local_path(value):
    if not isinstance(value, str) or not value.strip():
        raise ValueError("Expected a nonempty repository-relative path")
    value = value.replace("\\", "/")
    path = PurePosixPath(value)
    if path.is_absolute() or ".." in path.parts or ":" in value:
        raise ValueError(f"Not a repository-relative path: {value}")
    return path.as_posix()


def string_list(value, label):
    if not isinstance(value, list) or any(not isinstance(item, str) or not item.strip() for item in value):
        raise ValueError(f"{label} must be a list of nonempty strings")
    return value


def load_rules(root):
    data = json.loads((root / CONFIG).read_text(encoding="utf-8"))
    if not isinstance(data, dict) or data.get("version") != 1 or not isinstance(data.get("rules"), list):
        raise ValueError(f"{CONFIG}: expected version 1 and a rules list")
    seen = set()
    for rule in data["rules"]:
        if not isinstance(rule, dict) or set(rule) - {"id", "any", "all", "docs", "skills", "checks"}:
            raise ValueError(f"{CONFIG}: invalid rule fields")
        name = rule.get("id")
        if not isinstance(name, str) or not name.strip() or name in seen:
            raise ValueError(f"{CONFIG}: rule IDs must be nonempty and unique")
        seen.add(name)
        for key in ("any", "all", "docs", "skills"):
            for value in string_list(rule.get(key, []), f"{name}.{key}"):
                local_path(value)
        if not rule.get("any") and not rule.get("all"):
            raise ValueError(f"{name}: at least one any/all glob is required")
        checks = rule.get("checks", [])
        if not isinstance(checks, list):
            raise ValueError(f"{name}.checks must be a list")
        for check in checks:
            if not isinstance(check, dict) or set(check) != {"command", "targets"}:
                raise ValueError(f"{name}: each check needs command and targets")
            if not isinstance(check["command"], str) or not check["command"].strip():
                raise ValueError(f"{name}: empty check command")
            if not string_list(check["targets"], f"{name}.targets"):
                raise ValueError(f"{name}: check targets cannot be empty")
            for target in check["targets"]:
                local_path(target)
    return data["rules"]


def diff_paths(data):
    """Git -z emits separate status/path tokens; renames include both paths."""
    tokens = data.split(b"\0")
    paths = set()
    index = 0
    while index < len(tokens) and tokens[index]:
        status = tokens[index]
        count = 2 if status[:1] in (b"R", b"C") else 1
        index += 1
        if index + count > len(tokens) or any(not value for value in tokens[index:index + count]):
            raise ValueError("Incomplete Git name-status output")
        paths.update(os.fsdecode(value) for value in tokens[index:index + count])
        index += count
    return paths


def changed_paths(root, base=None):
    paths = set()
    if base is not None:
        # Resolve an argument as a revision, never as a Git option or shell text.
        commit = git(root, "rev-parse", "--verify", "--end-of-options", base + "^{commit}").decode().strip()
        ancestor = git(root, "merge-base", commit, "HEAD").decode().strip()
        paths.update(diff_paths(git(root, "diff", "--name-status", "-z", "--find-renames", ancestor, "HEAD", "--")))
    for args in (("--cached",), ()):
        paths.update(diff_paths(git(root, "diff", "--name-status", "-z", "--find-renames", *args, "--")))
    paths.update(os.fsdecode(value) for value in git(root, "ls-files", "--others", "--exclude-standard", "-z").split(b"\0") if value)
    return sorted(paths)


def matching_rules(rules, paths):
    def hit(pattern):
        return any(fnmatch.fnmatchcase(path, pattern) for path in paths)

    return [rule for rule in rules
            if (not rule.get("any") or any(hit(pattern) for pattern in rule["any"]))
            and all(hit(pattern) for pattern in rule.get("all", []))]


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=ROOT)
    source = parser.add_mutually_exclusive_group()
    source.add_argument("--files", nargs="+", help="Explicit repository-relative paths, including deleted paths")
    source.add_argument("--base", help="Compare merge-base(REF, HEAD)..HEAD and include local changes")
    args = parser.parse_args(argv)
    try:
        rules = load_rules(args.root)
        paths = sorted({local_path(path) for path in args.files}) if args.files else changed_paths(args.root, args.base)
        if not paths:
            print("No changed files; no additional guidance selected.")
            return 0
        print("Changed paths:")
        for path in paths:
            print("  " + path)
        selected = matching_rules(rules, paths)
        if not selected:
            print("No matching rules; start with AGENTS.md.")
            return 0
        print("\nMatching rules: " + ", ".join(rule["id"] for rule in selected))
        for label, key in (("Read local guidance", "docs"), ("Load skills when needed", "skills")):
            values = dict.fromkeys(value for rule in selected for value in rule.get(key, []))
            if values:
                print("\n" + label + ":")
                for value in values:
                    print("  " + value)
        print("\nSuggested checks (printed only; not executed):")
        for command in dict.fromkeys(check["command"] for rule in selected for check in rule.get("checks", [])):
            print("  " + command)
        return 0
    except (OSError, ValueError) as error:
        print(f"doc-context: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
