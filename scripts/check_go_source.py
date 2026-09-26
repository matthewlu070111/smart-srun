#!/usr/bin/env python3
"""Reject legacy device runtimes and accidentally tracked private/build files."""
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[1]


def inspect(paths):
    problems = []
    for name in paths:
        path = Path(name)
        if name.startswith(('root/usr/lib/smart_srun/', '.codex/', '.claude/', '.worktrees/')):
            problems.append(name)
        elif name.startswith('root/') and path.suffix == '.py':
            problems.append(name)
        elif name in ('root/usr/bin/srunnet', 'root/etc/smart-srun/config.json',
                      'root/etc/smart-srun/user-presets.json'):
            problems.append(name)
        elif any(part in ('__pycache__', '.pytest_cache', '.venv', 'node_modules') for part in path.parts):
            problems.append(name)
        elif path.suffix.lower() in ('.ipk', '.apk', '.har', '.key', '.pyc') or name.endswith(('coverage.out', '.verify-go-test.log')):
            problems.append(name)
    return sorted(set(problems))


def main():
    paths = subprocess.check_output(['git', 'ls-files', '-z'], cwd=ROOT).decode().split('\0')
    problems = inspect(paths)
    if problems:
        raise SystemExit('Unshippable or private tracked files:\n'+'\n'.join(problems))
    makefile = (ROOT/'Makefile').read_text()
    for line in makefile.splitlines():
        if 'DEPENDS' in line and any(word in line.lower() for word in ('python', 'nodejs')):
            raise SystemExit('Device dependency unexpectedly requires a host runtime')
    print('Go source layout: no legacy runtime, local credentials or build artifacts tracked')


if __name__ == '__main__':
    main()
