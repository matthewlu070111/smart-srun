"""Exercise guidance selection and link checks in isolated Git repositories."""
import contextlib
import io
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
sys.path.insert(0, str(ROOT / "scripts"))
import check_docs  # noqa: E402
import doc_context  # noqa: E402


class DocumentationTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.git("init", "-q")
        self.write("AGENTS.md", "# Local guidance\n")
        self.write("CLAUDE.md", "@AGENTS.md\n")
        self.write("scripts/check.py", "raise SystemExit(0)\n")
        self.rules = [{"id": "docs", "any": ["*.md"], "docs": ["AGENTS.md"],
                       "checks": [{"command": "python scripts/check.py", "targets": ["scripts/check.py"]}]}]
        self.save_rules()

    def git(self, *args):
        return subprocess.check_output(["git", "-C", str(self.root), *args], stderr=subprocess.PIPE)

    def write(self, name, content):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content, encoding="utf-8")
        return path

    def save_rules(self):
        self.write(doc_context.CONFIG, json.dumps({"version": 1, "rules": self.rules}))

    def commit(self):
        self.git("add", "--all")
        self.git("-c", "user.name=Synthetic Test", "-c", "user.email=test@example.invalid",
                 "commit", "-q", "-m", "fixture")
        return self.git("rev-parse", "HEAD").decode().strip()

    def run_main(self, main, *args):
        output, errors = io.StringIO(), io.StringIO()
        with contextlib.redirect_stdout(output), contextlib.redirect_stderr(errors):
            status = main(["--root", str(self.root), *args])
        return status, output.getvalue(), errors.getvalue()

    def test_base_and_dirty_changes_include_both_sides_of_renames_and_deletions(self):
        self.write(".gitignore", "ignored/\n")
        for name in ("old committed.txt", "old staged.txt", "staged.txt", "unstaged.txt", "deleted.txt"):
            self.write(name, name + "\n")
        base = self.commit()
        self.git("mv", "old committed.txt", "new committed.txt")
        self.write("committed.txt", "new commit\n")
        self.commit()
        self.git("mv", "old staged.txt", "new staged.txt")
        self.write("staged.txt", "staged edit\n")
        self.git("add", "staged.txt")
        self.write("unstaged.txt", "worktree edit\n")
        (self.root / "deleted.txt").unlink()
        self.write("new file.txt", "untracked\n")
        self.write("ignored/private.txt", "excluded\n")
        dirty = {"old staged.txt", "new staged.txt", "staged.txt", "unstaged.txt", "deleted.txt", "new file.txt"}
        self.assertEqual(set(doc_context.changed_paths(self.root)), dirty)
        self.assertEqual(set(doc_context.changed_paths(self.root, base)),
                         dirty | {"old committed.txt", "new committed.txt", "committed.txt"})

    def test_default_supports_unborn_repository_and_empty_worktree(self):
        self.git("add", "AGENTS.md")
        self.assertIn("AGENTS.md", doc_context.changed_paths(self.root))
        self.commit()
        status, output, error = self.run_main(doc_context.main)
        self.assertEqual((status, error), (0, ""))
        self.assertIn("No changed files", output)

    def test_base_compares_the_merge_base_not_unrelated_base_branch_changes(self):
        ancestor = self.commit()
        self.git("checkout", "-q", "-b", "base")
        self.write("base-only.md", "Base branch change\n")
        base = self.commit()
        self.git("checkout", "-q", "-b", "work", ancestor)
        self.write("work-only.md", "Work branch change\n")
        self.commit()
        self.assertEqual(doc_context.changed_paths(self.root, base), ["work-only.md"])

    def test_base_is_resolved_without_treating_it_as_an_option(self):
        self.commit()
        status, _, error = self.run_main(doc_context.main, "--base=--help")
        self.assertEqual(status, 1)
        self.assertIn("doc-context:", error)

    def test_rules_drive_any_all_selection_and_commands_are_only_printed(self):
        self.rules.extend([
            {"id": "core", "any": ["core/**"], "docs": ["AGENTS.md"]},
            {"id": "cross", "all": ["core/**", "root/**"], "docs": ["AGENTS.md"],
             "checks": [{"command": "DO NOT EXECUTE THIS COMMAND", "targets": ["scripts/check.py"]}]},
        ])
        self.save_rules()
        rules = doc_context.load_rules(self.root)
        self.assertEqual([rule["id"] for rule in doc_context.matching_rules(rules, ["core/a.go"])], ["core"])
        status, output, error = self.run_main(doc_context.main, "--files", "core/a.go", "root/b.lua")
        self.assertEqual((status, error), (0, ""))
        self.assertIn("Matching rules: core, cross", output)
        self.assertIn("DO NOT EXECUTE THIS COMMAND", output)
        self.assertIn("printed only; not executed", output)

    def test_repository_routes_readme_and_documentation_tools_without_release_guidance(self):
        rules = doc_context.load_rules(ROOT)
        selected = doc_context.matching_rules(rules, ["README.md"])
        self.assertEqual({rule["id"] for rule in selected}, {"documentation"})
        for path in ("scripts/check_docs.py", "scripts/doc_context.py"):
            with self.subTest(path=path):
                selected = doc_context.matching_rules(rules, [path])
                self.assertEqual({rule["id"] for rule in selected}, {"host-tools", "documentation"})
                self.assertFalse(any(rule.get("skills") for rule in selected))
                self.assertIn("scripts/AGENTS.md", [value for rule in selected for value in rule.get("docs", [])])

    def test_repository_go_tests_do_not_request_the_host_test_suite(self):
        selected = doc_context.matching_rules(doc_context.load_rules(ROOT), ["core/internal/policy/state_test.go"])
        self.assertEqual({rule["id"] for rule in selected}, {"go-core"})
        self.assertFalse(any("unittest" in check["command"] for rule in selected for check in rule.get("checks", [])))

    def test_repository_release_routing_retains_deleted_and_renamed_paths(self):
        self.write("scripts/build_go_sdk.py", "# synthetic build script\n")
        self.write("targets.json", "{}\n")
        self.commit()
        self.git("mv", "scripts/build_go_sdk.py", "scripts/check_docs.py")
        (self.root / "targets.json").unlink()
        paths = doc_context.changed_paths(self.root)
        self.assertEqual(set(paths), {"scripts/build_go_sdk.py", "scripts/check_docs.py", "targets.json"})
        selected = doc_context.matching_rules(doc_context.load_rules(ROOT), paths)
        self.assertIn("build-and-release", {rule["id"] for rule in selected})

    def test_markdown_tracks_new_files_and_ignores_private_and_code_examples(self):
        self.write("guide/space name.md", "# Target\n")
        self.write("guide/a(b).md", "# Parentheses\n")
        self.write("README.md", "\n".join([
            "[Space](guide/space%20name.md#section)",
            "[Angle](<guide/space name.md>)",
            "[Paren](guide/a(b).md)",
            "[Directory](guide/)",
            "[Reference][guide]", "[guide]: guide/space%20name.md \"title\"",
            "<img src='guide/space%20name.md'>",
            "[Web](https://example.invalid/missing)", "[Anchor](#missing)",
            "[Absolute site route](/guide/missing)", "[Mail](mailto:test@example.invalid)",
            "`[Inline example](missing.md)`", "```md", "[Example](missing.md)", "```",
        ]))
        self.commit()
        self.write("new.md", "[New document](AGENTS.md)\n")
        self.write(".codex/private.md", "[Private](missing.md)\n")
        self.write("node_modules/cache.md", "[Cache](missing.md)\n")
        self.write(".gitignore", "ignored/\n")
        self.write("ignored/notes.md", "[Ignored](missing.md)\n")
        status, output, errors = self.run_main(check_docs.main)
        self.assertEqual((status, errors), (0, ""))
        self.assertIn("6 Markdown files", output)
        self.assertIn(Path("new.md"), check_docs.markdown_paths(self.root))

    def test_missing_markdown_targets_and_claude_import_fail(self):
        self.write("README.md", "[Broken](removed.md#section)\n")
        self.write("CLAUDE.md", "@missing-AGENTS.md\n")
        status, _, errors = self.run_main(check_docs.main)
        self.assertEqual(status, 1)
        self.assertIn("README.md:1: missing relative link target: removed.md#section", errors)
        self.assertIn("CLAUDE.md:1: missing relative link target: missing-AGENTS.md", errors)

    def test_deleted_tracked_document_breaks_referring_link(self):
        target = self.write("guide.md", "# Guide\n")
        self.write("README.md", "[Guide](guide.md)\n")
        self.commit()
        target.unlink()
        status, _, errors = self.run_main(check_docs.main)
        self.assertEqual(status, 1)
        self.assertIn("missing relative link target: guide.md", errors)

    def test_missing_rule_docs_skills_and_check_targets_fail(self):
        self.rules[0]["docs"] = ["removed.md"]
        self.rules[0]["skills"] = [".agents/skills/missing/SKILL.md"]
        self.rules[0]["checks"][0]["targets"] = ["scripts/missing.py"]
        self.save_rules()
        status, _, errors = self.run_main(check_docs.main)
        self.assertEqual(status, 1)
        for name in ("removed.md", ".agents/skills/missing/SKILL.md", "scripts/missing.py"):
            self.assertIn(name, errors)

    def test_invalid_rule_shape_or_traversal_is_rejected(self):
        for rule in ({"id": "bad", "any": "*.md"},
                     {"id": "bad", "any": ["*.md"], "docs": ["../outside.md"]},
                     {"id": "bad", "any": ["*.md"], "checks": [{"command": "anything", "targets": []}]}):
            with self.subTest(rule=rule):
                self.rules = [rule]
                self.save_rules()
                status, _, errors = self.run_main(check_docs.main)
                self.assertEqual(status, 1)
                self.assertIn("check-docs:", errors)


if __name__ == "__main__":
    unittest.main()
