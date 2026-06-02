import os
import sys
import time
import tempfile
import unittest


REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MODULE_ROOT = os.path.join(REPO_ROOT, "root", "usr", "lib", "smart_srun")

if MODULE_ROOT not in sys.path:
    sys.path.insert(0, MODULE_ROOT)


import logger


class LoggerTests(unittest.TestCase):
    def setUp(self):
        logger.clear_log_context()
        logger.set_log_threshold("INFO")
        self._emitted = []
        self._orig_write = logger._write_log
        logger._write_log = self._emitted.append

    def tearDown(self):
        logger._write_log = self._orig_write
        logger.clear_log_context()
        logger.set_log_threshold("INFO")

    def test_threshold_filters_lower_levels(self):
        logger.set_log_threshold("WARN")
        logger.log("DEBUG", "d")
        logger.log("INFO", "i")
        logger.log("WARN", "w")
        logger.log("ERROR", "e")
        self.assertEqual(len(self._emitted), 2)
        self.assertIn("WARN w", self._emitted[0])
        self.assertIn("ERROR e", self._emitted[1])

    def test_all_threshold_emits_every_level(self):
        logger.set_log_threshold("ALL")
        logger.log("DEBUG", "d")
        logger.log("INFO", "i")
        logger.log("WARN", "w")
        logger.log("ERROR", "e")
        self.assertEqual(len(self._emitted), 4)

    def test_all_level_on_call_is_never_emitted(self):
        logger.set_log_threshold("ALL")
        logger.log("ALL", "meaningless")
        self.assertEqual(self._emitted, [])

    def test_unknown_level_falls_back_to_info(self):
        logger.set_log_threshold("bogus")
        self.assertEqual(logger.get_log_threshold(), "INFO")
        logger.log("DEBUG", "d")
        logger.log("INFO", "i")
        self.assertEqual(len(self._emitted), 1)

    def test_unknown_level_on_call_is_treated_as_info(self):
        logger.log("whatever", "evt")
        self.assertEqual(len(self._emitted), 1)
        self.assertIn("INFO evt", self._emitted[0])

    def test_context_is_merged_and_per_call_overrides_it(self):
        logger.set_log_context(op_id="abc")
        logger.log("INFO", "e", key="v")
        logger.log("INFO", "e2", op_id="override")
        self.assertIn("op_id=abc", self._emitted[0])
        self.assertIn("key=v", self._emitted[0])
        self.assertIn("op_id=override", self._emitted[1])
        self.assertNotIn("op_id=abc", self._emitted[1])

    def test_clear_log_context_removes_keys(self):
        logger.set_log_context(op_id="abc", other="x")
        logger.clear_log_context("op_id")
        logger.log("INFO", "e")
        self.assertNotIn("op_id=", self._emitted[0])
        self.assertIn("other=x", self._emitted[0])

        logger.clear_log_context()
        logger.log("INFO", "e2")
        self.assertNotIn("other=", self._emitted[1])

    def test_value_quoting_for_spaces_and_empty(self):
        logger.log("INFO", "e", msg_key="has spaces", empty="")
        line = self._emitted[0]
        self.assertIn('msg_key="has spaces"', line)
        self.assertIn('empty=""', line)

    def test_control_characters_are_kept_on_one_log_line(self):
        logger.log("INFO", "e", "first\nsecond", detail="a\tb", raw="x\\y")
        line = self._emitted[0]
        self.assertIn("first\\nsecond", line)
        self.assertIn("detail=a\\tb", line)
        self.assertIn("raw=x\\\\y", line)
        self.assertNotIn("\n", line)
        self.assertNotIn("\t", line)

    def test_append_log_keeps_legacy_lines_single_line(self):
        logger.append_log("legacy\nmessage\twith controls")
        line = self._emitted[0]
        self.assertIn("legacy\\nmessage\\twith controls", line)
        self.assertNotIn("\n", line)
        self.assertNotIn("\t", line)

    def test_sensitive_structured_fields_are_redacted(self):
        logger.set_log_context(session_token="context-secret")
        logger.log(
            "INFO",
            "e",
            password="plain-secret",
            chksum="deadbeef",
            campus_key="wifi-secret",
            username="alice",
        )
        line = self._emitted[0]
        self.assertIn("password=***", line)
        self.assertIn("chksum=***", line)
        self.assertIn("campus_key=***", line)
        self.assertIn("session_token=***", line)
        self.assertIn("username=alice", line)
        self.assertNotIn("plain-secret", line)
        self.assertNotIn("deadbeef", line)
        self.assertNotIn("wifi-secret", line)
        self.assertNotIn("context-secret", line)

    def test_timed_context_manager_reports_milliseconds(self):
        with logger.timed() as t:
            time.sleep(0.01)
        self.assertGreaterEqual(t.ms, 5)

    def test_rotate_log_tail_keeps_recent_complete_lines(self):
        original_file = logger.LOG_FILE
        original_max = logger.LOG_MAX_BYTES
        self.addCleanup(setattr, logger, "LOG_FILE", original_file)
        self.addCleanup(setattr, logger, "LOG_MAX_BYTES", original_max)

        with tempfile.NamedTemporaryFile(delete=False) as tmp:
            path = tmp.name
        self.addCleanup(lambda: os.path.exists(path) and os.remove(path))

        logger.LOG_FILE = path
        logger.LOG_MAX_BYTES = 80
        with open(path, "wb") as f:
            for idx in range(10):
                f.write(("line-%02d payload\n" % idx).encode("utf-8"))

        logger._rotate_log_tail()

        with open(path, "rb") as f:
            data = f.read().decode("utf-8")
        self.assertTrue(data.startswith("line-"))
        self.assertIn("line-09 payload\n", data)
        self.assertNotIn("line-00 payload\n", data)
        self.assertLessEqual(len(data.encode("utf-8")), logger.LOG_MAX_BYTES // 2)


if __name__ == "__main__":
    unittest.main()
