import inspect
import os
import sys
import unittest


REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MODULE_ROOT = os.path.join(REPO_ROOT, "root", "usr", "lib", "smart_srun")

if MODULE_ROOT not in sys.path:
    sys.path.insert(0, MODULE_ROOT)


import crypto


class CryptoRegressionTests(unittest.TestCase):
    def test_get_xencode_masks_additions_before_bitand(self):
        source = inspect.getsource(crypto.get_xencode)

        self.assertIn("d_val = (d_val + DELTA) & MASK_32", source)
        self.assertIn("pwd[p_val] = (pwd[p_val] + m_val) & MASK_32", source)
        self.assertIn("pwd[n_val] = (pwd[n_val] + m_val) & MASK_32", source)


if __name__ == "__main__":
    unittest.main()
