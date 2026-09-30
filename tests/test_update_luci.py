"""Execute shipped update UI handlers with deterministic DOM/XHR fixtures."""
from pathlib import Path
import subprocess
import unittest

ROOT = Path(__file__).resolve().parents[1]


class UpdateLuciTests(unittest.TestCase):
    def test_update_retry_and_request_completion(self):
        subprocess.run(['node', str(ROOT / 'tests/update_luci.js'),
                        str(ROOT / 'root/www/luci-static/resources/smart_srun.js')], check=True)


if __name__ == '__main__':
    unittest.main()
