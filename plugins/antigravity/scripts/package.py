#!/usr/bin/env python3
"""Create a standalone adapter package; generation is not host validation."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import runpy
import shutil
import tempfile

FILES = ('plugin.json', 'mcp_config.json', 'README.md',
         'skills/governance-workflow/SKILL.md', 'scripts/install.py')


def package(destination, selection=None):
    root = Path(__file__).resolve().parents[1]
    shared = root.parent / 'codex/scripts/serve.py'
    destination = Path(destination)
    if not destination.is_absolute():
        raise ValueError('explicit absolute output path required')
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
        if selection is not None:
            selection = Path(selection)
            installer = runpy.run_path(str(stage / 'scripts/install.py'))
            # Validate using the staged shared bootstrap, but bind the final
            # path: the host must never try to launch the temporary directory.
            entry = installer['mcp_entry'](stage, selection)
            entry['args'][0] = str(destination / 'scripts/serve.py')
            (stage / 'mcp_config.json').write_text(json.dumps({'mcpServers': {'dacli': entry}}, indent=2) + '\n')
        names = (*FILES, 'scripts/serve.py')
        hashes = {name: hashlib.sha256((stage / name).read_bytes()).hexdigest() for name in names}
        (stage / 'package-manifest.json').write_text(json.dumps({
            'schema_version': 1, 'adapter': 'antigravity', 'version': '0.5.0',
            'files': hashes, 'shared_bootstrap_sha256': hashes['scripts/serve.py'],
            'host_validated': False, 'orchestration_supported': False,
            'configured': selection is not None,
        }, indent=2) + '\n')
        os.rename(stage, destination)
        return {'package': str(destination), 'files': hashes,
                'generation_only': True, 'host_validated': False,
                'configured': selection is not None}
    finally:
        if stage.exists():
            shutil.rmtree(stage)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', required=True)
    parser.add_argument('--selection', help='absolute private selection; render a host-ready package at its final path')
    args = parser.parse_args()
    try:
        print(json.dumps(package(args.output, args.selection), indent=2))
    except (OSError, ValueError) as error:
        parser.exit(3, 'dacli adapter packaging refused: ' + str(error) + '\n')
