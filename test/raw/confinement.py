"""Validate output trees before writes or cleanup; native inputs are read-only."""
import os
import pathlib
import sys

root = pathlib.Path(sys.argv[1]).resolve()
work = root / '.sdd-work'
try:
    if work.is_symlink():
        raise ValueError(str(work))
    # Check canonical destinations, including dangling links and alternate modfiles.
    for name in ('tmp', 'cache', 'mod', 'gopath', 'collections', 'assets', 'logs',
                 'docker', 'legacy', 'model-smoke', 'localai', 'localai-smoke.mod', 'localai-smoke.sum',
                 'localai.tar.gz'):
        top = work / name
        paths = [top]
        if top.is_dir() and not top.is_symlink():
            for directory, dirs, files in os.walk(top, followlinks=False):
                paths.extend(pathlib.Path(directory) / n for n in dirs + files
                             if (pathlib.Path(directory) / n).is_symlink())
        for path in paths:
            if not path.resolve().is_relative_to(work):
                raise ValueError(str(path))
except (ValueError, OSError, RuntimeError) as error:
    sys.exit('unsafe output: ' + str(error))
