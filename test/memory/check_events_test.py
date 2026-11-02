"""Fixture-free negative tests for the PostgreSQL acceptance manifest."""
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

CHECK = Path(__file__).with_name('check_events.py')
PREFIX = 'TestMemoryDeletePostgres'
PACKAGE = 'github.com/mudler/localrecall/'


def events():
    names = [('rag', 'AssetCleanupAndLegacy')]
    names += [('rag/engine', name) for name in
              ('ExactReplayReplica', 'TransactionBoundaries', 'WriteTakeover', 'PausedWriter', 'MaintenanceReservation')]
    names += [('rag/engine', 'TransactionBoundaries/' + child)
              for child in ('chunks', 'manifest', 'commit')]
    return [dict(Action=action, Package=PACKAGE + pkg, Test=PREFIX + name)
            for pkg, name in names for action in ('run', 'pass')]


class GateTests(unittest.TestCase):
    def check(self, data, args=None, prefix=''):
        with tempfile.NamedTemporaryFile(mode='w') as log:
            for event in data:
                log.write(json.dumps(event) + '\n')
            log.flush()
            return subprocess.run([sys.executable, str(CHECK), log.name, prefix] +
                                  (args or ['-run', '^' + PREFIX]), capture_output=True)

    def test_exact_delete_autodetection(self):
        self.assertEqual(self.check(events()).returncode, 0)
        self.assertEqual(self.check(events(), ['-run=^' + PREFIX]).returncode, 0)

    def test_wrong_prefix(self):
        self.assertNotEqual(self.check(events(), prefix='TestMemoryPostgres').returncode, 0)
        self.assertNotEqual(self.check(events(), prefix='Unknown').returncode, 0)

    def test_zero_tests(self):
        self.assertNotEqual(self.check([]).returncode, 0)

    def test_skip(self):
        self.assertNotEqual(self.check(events() + [dict(Action='skip', Package=PACKAGE+'rag')]).returncode, 0)

    def test_missing_required_boundary(self):
        self.assertNotEqual(self.check([e for e in events() if not e['Test'].endswith('/commit')]).returncode, 0)

    def test_incomplete(self):
        self.assertNotEqual(self.check(events()[:-1]).returncode, 0)

    def test_ambiguous_selectors(self):
        for args in (['-run', '^TestMemory'], ['-run', '^'+PREFIX+'$'],
                     ['-run', '^'+PREFIX, '-run', '^TestMemoryPostgres'],
                     ['-run', '^TestMemory(Postgres|DeletePostgres)']):
            self.assertNotEqual(self.check(events(), args).returncode, 0)


if __name__ == '__main__':
    unittest.main()
