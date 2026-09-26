"""Every event the daemon can log has Chinese wording in the page.

Spec 02 splits the log in two: Go owns the event codes and their fields, LuCI
owns how they read. That only works if neither half can move without the other,
and the failure is quiet -- an untranslated event reaches a user as a bare
`quiet_logout_queued`, which is exactly the raw-code output the friendly
renderer exists to prevent.

Source-level string checks are the right tool here: this is a dictionary
contract between two files, not behaviour that could be observed by running
something.
"""

import re
import unittest
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[1]
EVENTS_GO = REPO_ROOT / "core/internal/logstore/events.go"
CONTROLLER = REPO_ROOT / "root/usr/lib/lua/luci/controller/smart_srun.lua"


def go_event_names():
    """Resolve the catalogue's entries to the strings they are declared with."""
    source = EVENTS_GO.read_text(encoding="utf-8")
    constants = dict(
        re.findall(r'^\t(Event[A-Za-z]+)\s*=\s*"([^"]+)"', source, re.MULTILINE)
    )
    catalogue = re.search(r"var catalogue = \[\]Event\{(.*?)\n\}", source, re.S)
    assert catalogue, "the catalogue is not where this test expects it"
    used = re.findall(r"\{(Event[A-Za-z]+),", catalogue.group(1))
    assert used, "no catalogue entries found"
    return constants, {constants[name] for name in used}


def lua_translations():
    source = CONTROLLER.read_text(encoding="utf-8")
    table = re.search(r"local event_zh = \{(.*?)\n\}", source, re.S)
    assert table, "event_zh is not where this test expects it"
    return dict(
        re.findall(r"^\s*([a-z_0-9]+)\s*=\s*\"([^\"]*)\"", table.group(1), re.MULTILINE)
    )


class LogEventTranslationTests(unittest.TestCase):
    def test_every_catalogued_event_has_chinese_wording(self):
        _, events = go_event_names()
        translations = lua_translations()
        missing = sorted(events - set(translations))
        self.assertEqual(
            missing, [],
            "these events would reach a user as raw codes: %s" % missing,
        )
        for event in sorted(events):
            with self.subTest(event=event):
                wording = translations[event]
                self.assertNotEqual(wording.strip(), "")
                self.assertTrue(
                    any(ord(char) > 0x2E80 for char in wording),
                    "%s is translated as %r, which is not Chinese" % (event, wording),
                )

    def test_every_declared_constant_is_in_the_catalogue(self):
        """A constant outside the catalogue has no level and no translation check."""
        constants, events = go_event_names()
        orphans = sorted(set(constants.values()) - events)
        self.assertEqual(orphans, [], "declared but not catalogued: %s" % orphans)

    def test_the_page_may_know_wording_the_daemon_does_not_emit_yet(self):
        """The reverse direction is deliberately not required.

        The Lua table still carries the baseline's events -- the wireless and
        gateway ones that arrive with M12 and M14 -- and dropping them to make
        the two sides match would delete wording that is about to be needed.
        """
        _, events = go_event_names()
        translations = lua_translations()
        self.assertGreater(
            len(set(translations) - events), 10,
            "the baseline wording seems to have been deleted rather than kept",
        )


if __name__ == "__main__":
    unittest.main()
