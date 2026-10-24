#!/usr/bin/env python3
"""Check local Markdown links and review-context targets without network access."""
import argparse
import os
from pathlib import Path
import re
import sys
from urllib.parse import unquote, urlsplit

from doc_context import CONFIG, ROOT, git, load_rules

EXCLUDED = {".git", ".codex", ".claude", ".worktrees", ".cache", ".pytest_cache",
            ".ruff_cache", "__pycache__", "node_modules", ".venv"}
INLINE_LINK = re.compile(r"!?\[[^\]\n]*\]\(\s*(?:<([^>\n]+)>|((?:\\.|[^()\s]|\([^()\n]*\))+))(?:\s+(?:\"[^\"]*\"|'[^']*'|\([^)]*\)))?\s*\)")
REFERENCE_LINK = re.compile(r"^\s{0,3}\[[^\]\n]+\]:\s*(?:<([^>\n]+)>|(\S+))", re.MULTILINE)
HTML_LINK = re.compile(r"\b(?:href|src)\s*=\s*['\"]([^'\"]+)['\"]", re.IGNORECASE)


def markdown_paths(root):
    files = git(root, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
    return sorted({Path(os.fsdecode(name)) for name in files.split(b"\0") if name
                   and Path(os.fsdecode(name)).suffix.lower() == ".md"
                   and not (set(Path(os.fsdecode(name)).parts) & EXCLUDED)
                   and (root / os.fsdecode(name)).is_file()})


def prose(text):
    """Ignore examples in fenced and inline code, preserving source line numbers."""
    lines = []
    fence = None
    for line in text.splitlines(keepends=True):
        marker = re.match(r"^ {0,3}(`{3,}|~{3,})(.*)$", line.rstrip("\r\n"))
        if fence:
            if marker and marker[1][0] == fence[0] and len(marker[1]) >= len(fence) and not marker[2].strip():
                fence = None
            lines.append("\n" if line.endswith("\n") else "")
        elif marker:
            fence = marker[1]
            lines.append("\n" if line.endswith("\n") else "")
        else:
            lines.append(re.sub(r"(`+).*?\1", "", line))
    return "".join(lines)


def link_error(root, source, target):
    target = re.sub(r"\\([\\`*{}\[\]()#+\-.!_> ])", r"\1", target)
    parsed = urlsplit(target)
    if parsed.scheme or parsed.netloc or not parsed.path or target.startswith("/"):
        return None
    destination = (root / source.parent / unquote(parsed.path)).resolve()
    if not destination.is_relative_to(root.resolve()):
        return f"relative link leaves repository: {target}"
    if not destination.exists():
        return f"missing relative link target: {target}"
    return None


def check_markdown(root, source):
    text = prose((root / source).read_text(encoding="utf-8"))
    links = []
    for pattern in (INLINE_LINK, REFERENCE_LINK):
        links.extend((match.start(), match[1] or match[2]) for match in pattern.finditer(text))
    links.extend((match.start(), match[1]) for match in HTML_LINK.finditer(text))
    if source.name == "CLAUDE.md":
        links.extend((match.start(), match[1]) for match in re.finditer(r"^@([^\s]+)\s*$", text, re.MULTILINE))
    errors = []
    for position, target in links:
        error = link_error(root, source, target)
        if error:
            line = text.count("\n", 0, position) + 1
            errors.append(f"{source.as_posix()}:{line}: {error}")
    return errors


def check_rules(root):
    errors = []
    for rule in load_rules(root):
        targets = [(path, True) for key in ("docs", "skills") for path in rule.get(key, [])]
        targets.extend((path, False) for check in rule.get("checks", []) for path in check["targets"])
        for path, document in targets:
            target = (root / path).resolve()
            if not target.is_relative_to(root.resolve()) or not (target.is_file() if document else target.exists()):
                errors.append(f"{CONFIG}: {rule['id']}: missing or nonlocal target: {path}")
    return errors


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--root", type=Path, default=ROOT)
    args = parser.parse_args(argv)
    try:
        paths = markdown_paths(args.root)
        errors = [error for path in paths for error in check_markdown(args.root, path)]
        errors.extend(check_rules(args.root))
        if errors:
            print("\n".join(errors), file=sys.stderr)
            return 1
        print(f"Documentation: {len(paths)} Markdown files and review-context targets checked (offline).")
        return 0
    except (OSError, ValueError) as error:
        print(f"check-docs: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
