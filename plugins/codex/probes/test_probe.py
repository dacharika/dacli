"""Runner unit tests, not native identity or hook evidence."""
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

import probe


class RunnerTests(unittest.TestCase):
    def test_process_bound(self):
        with tempfile.TemporaryDirectory() as tmp:
            result = probe.run([sys.executable, '-c', 'import time; time.sleep(20)'], {}, tmp, seconds=0.1)
        self.assertTrue(result[3])
        self.assertEqual(result[0], -9)
        self.assertLess(result[4], 3)

    def test_version_mismatch_refuses_before_cases(self):
        binary = os.environ.get('PROBE_CODEX') or shutil.which('codex')
        if not binary:
            self.skipTest('installed Codex unavailable')
        result = subprocess.run([sys.executable, str(Path(probe.__file__)), '--codex', binary,
                                 '--expected-version', 'deliberately-wrong', '--case', 'observe'],
                                capture_output=True, text=True, timeout=12)
        self.assertEqual(result.returncode, 2)
        self.assertIn('host version mismatch', result.stderr)
        self.assertEqual(result.stdout, '')


if __name__ == '__main__':
    unittest.main()
