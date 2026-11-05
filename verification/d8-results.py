#!/usr/bin/env python3
"""Fail closed on skipped/empty Go acceptance runs; retain named assertion evidence."""
import json
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
events = [json.loads(line) for line in path.read_text().splitlines() if line.startswith('{')]
passed = [e['Test'] for e in events if e.get('Action') == 'pass' and e.get('Test')]
failed = [e.get('Test', e.get('Package')) for e in events if e.get('Action') in ('skip', 'fail')]
if not passed or failed or not any(e.get('Action') == 'pass' and not e.get('Test') for e in events):
    raise SystemExit(f'{path}: empty, failed or skipped acceptance: {failed}')
result = {'source': str(path), 'passedTestsAndGroups': passed, 'count': len(passed)}
path.with_suffix('.summary.json').write_text(json.dumps(result, ensure_ascii=False, indent=2) + '\n')
print(f'{path.stem}: {len(passed)} tests/groups passed, no skips')
