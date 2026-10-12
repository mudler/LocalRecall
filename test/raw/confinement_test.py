"""Every public runner must reject escaping outputs before its first write."""
import os
import pathlib
import shutil
import subprocess
import tempfile
import unittest


class Confinement(unittest.TestCase):
    def test_runner_entrypoints(self):
        source = pathlib.Path('test/raw').resolve()
        cases = ('model-smoke/backends', 'localai/core/services/agentpool',
                 'localai-smoke.mod', 'logs/nested', 'docker/contexts',
                 'cache/nested', 'mod/nested', 'gopath/pkg', 'tmp/nested',
                 'collections/nested', 'assets/nested', 'legacy/owner/assets')
        for runner in ('verify.sh', 'database.sh', 'localai-smoke.sh', 'legacy.sh'):
            for nested in cases:
                with self.subTest(runner=runner, nested=nested), tempfile.TemporaryDirectory(dir='.sdd-work/tmp') as tmp:
                    root = pathlib.Path(tmp).resolve()
                    shutil.copytree(source, root / 'test/raw')
                    (root / 'go.mod').write_text('module fixture\n')
                    (root / 'rag').mkdir()
                    work = root / '.sdd-work'
                    work.mkdir()
                    outside = root / 'outside'
                    outside.mkdir()
                    sentinel = outside / 'sentinel'
                    sentinel.write_text('unchanged')
                    link = work / nested
                    link.parent.mkdir(parents=True, exist_ok=True)
                    link.symlink_to(outside, target_is_directory=True)
                    before = sorted(str(p.relative_to(root)) for p in root.rglob('*'))
                    env = dict(os.environ)
                    env.pop('LOCALRECALL_TEST_DATABASE_URL', None)
                    result = subprocess.run(['sh', 'test/raw/' + runner], cwd=root,
                                            env=env, capture_output=True, timeout=10)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn(b'unsafe output', result.stderr)
                    self.assertEqual(sentinel.read_text(), 'unchanged')
                    self.assertEqual(before, sorted(str(p.relative_to(root)) for p in root.rglob('*')))


if __name__ == '__main__':
    unittest.main()
