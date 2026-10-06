#!/usr/bin/env python3
"""Create a standalone adapter package; generation is not host validation."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import tempfile

FILES = ('plugin.json', 'mcp_config.json', 'README.md',
         'skills/governance-workflow/SKILL.md', 'scripts/install.py')


def package(destination):
    root = Path(__file__).resolve().parents[1]
    shared = root.parent / 'codex/scripts/serve.py'
    destination = Path(destination).absolute()
    if destination.exists() or destination.is_symlink():
        raise ValueError('destination must be absent')
    destination.parent.mkdir(parents=True, exist_ok=True)
    stage = Path(tempfile.mkdtemp(prefix='.dacli-antigravity-', dir=destination.parent))
    try:
        for name in FILES:
            target = stage / name
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes((root / name).read_bytes())
        (stage / 'scripts/serve.py').write_bytes(shared.read_bytes())
        names = (*FILES, 'scripts/serve.py')
        hashes = {name: hashlib.sha256((stage / name).read_bytes()).hexdigest() for name in names}
        (stage / 'package-manifest.json').write_text(json.dumps({
            'schema_version': 1, 'adapter': 'antigravity', 'version': '0.5.0',
            'files': hashes, 'shared_bootstrap_sha256': hashes['scripts/serve.py'],
            'host_validated': False, 'orchestration_supported': False,
        }, indent=2) + '\n')
        os.rename(stage, destination)
        return {'package': str(destination), 'files': hashes,
                'generation_only': True, 'host_validated': False}
    finally:
        if stage.exists():
            shutil.rmtree(stage)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', required=True)
    args = parser.parse_args()
    try:
        print(json.dumps(package(args.output), indent=2))
    except (OSError, ValueError) as error:
        parser.exit(3, 'dacli adapter packaging refused: ' + str(error) + '\n')
