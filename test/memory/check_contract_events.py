"""Fail closed on missing named tests, skips, failures, and incomplete JSON runs."""
import json
from pathlib import Path
import re
import sys

required = json.loads(Path(__file__).with_name('required_tests.json').read_text())
runs, passes, packages = set(), set(), set()
for filename in sys.argv[1:]:
    for line in open(filename):
        try:
            e = json.loads(line)
        except ValueError:
            sys.exit('non-JSON test output: ' + line[:200])
        action = e.get('Action')
        if action in ('skip', 'fail'):
            sys.exit('skip/failure is not acceptance: ' + line)
        # Legacy Ginkgo reports spec skips inside Output, not Go skip events.
        if re.search(r'\b[1-9][0-9]*\s+Skipped\b', e.get('Output', '')):
            sys.exit('Ginkgo skipped specs are not acceptance')
        key = (e.get('Package'), e.get('Test'))
        if e.get('Test'):
            if action == 'run': runs.add(key)
            if action == 'pass': passes.add(key)
        elif action == 'pass': packages.add(e.get('Package'))
if not runs or runs != passes:
    sys.exit('zero or incomplete test execution')
for pkg, names in required.items():
    package = 'github.com/mudler/localrecall/' + pkg
    if package not in packages: sys.exit('missing package pass: ' + package)
    for name in names:
        if (package, name) not in passes:
            sys.exit('required named test missing: ' + pkg + '/' + name)
for child in ('chromem', 'postgres'):
    if ('github.com/mudler/localrecall/rag', 'TestMemoryContractConsumer/' + child) not in passes:
        sys.exit('real public consumer contract missing: ' + child)
print('Acceptance: %d tests/subtests, %d packages; all named tests passed, no skips' % (len(passes), len(packages)))
