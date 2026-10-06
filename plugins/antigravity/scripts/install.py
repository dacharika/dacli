#!/usr/bin/env python3
"""Render an MCP entry; modify configuration only with explicit --install."""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import stat
import tempfile

FILES = {'plugin.json', 'mcp_config.json', 'README.md',
         'skills/governance-workflow/SKILL.md', 'scripts/install.py', 'scripts/serve.py'}
MAX_BYTES = 1024 * 1024


def read(path, private=False):
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    with os.fdopen(fd, 'rb') as stream:
        info = os.fstat(stream.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid():
            raise ValueError('owned regular file required')
        if private and info.st_mode & 0o077:
            raise ValueError('private file required')
        raw = stream.read(MAX_BYTES + 1)
    if len(raw) > MAX_BYTES:
        raise ValueError('oversized file')
    return raw


def unique(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError('duplicate JSON key')
        result[key] = value
    return result


def load(raw):
    return json.loads(raw, object_pairs_hook=unique)


def render(root, selection, config):
    manifest = load(read(root / 'package-manifest.json'))
    if (manifest.get('schema_version') != 1 or manifest.get('adapter') != 'antigravity'
            or manifest.get('version') != '0.5.0' or set(manifest.get('files', {})) != FILES):
        raise ValueError('unknown package source contract')
    for name, digest in manifest['files'].items():
        if hashlib.sha256(read(root / name)).hexdigest() != digest:
            raise ValueError('package source changed')
    if manifest.get('shared_bootstrap_sha256') != manifest['files']['scripts/serve.py']:
        raise ValueError('shared bootstrap binding missing')
    # Reuse the shared descriptor-bound selection validator. It reads paths,
    # never credential contents, and this function never invokes launch().
    spec = importlib.util.spec_from_file_location('dacli_bootstrap', root / 'scripts/serve.py')
    bootstrap = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(bootstrap)
    chosen = bootstrap.selection(selection)
    if 'orchestration_config_file' in chosen:
        raise ValueError('Antigravity orchestration capability adapter unsupported')
    old = read(config) if config.exists() or config.is_symlink() else None
    document = load(old) if old is not None else {}
    if not isinstance(document, dict) or not isinstance(document.get('mcpServers', {}), dict):
        raise ValueError('unknown MCP configuration shape')
    template = load(read(root / 'mcp_config.json'))
    entry = template['mcpServers']['dacli']
    if entry != {'command': 'python3', 'args': ['@DACLI_PACKAGE_ROOT@/scripts/serve.py', '@DACLI_SELECTION_FILE@']}:
        raise ValueError('unknown MCP template')
    desired = {'command': 'python3', 'args': [str(root / 'scripts/serve.py'), str(selection)]}
    servers = dict(document.get('mcpServers', {}))
    if 'dacli' in servers and servers['dacli'] != desired:
        raise ValueError('conflicting dacli server; preserve existing configuration')
    servers['dacli'] = desired
    document['mcpServers'] = servers
    return old, (json.dumps(document, indent=2) + '\n').encode()


def atomic_install(config, old, updated):
    if not config.parent.is_dir():
        raise ValueError('configuration parent must already exist')
    backup = None
    if old is not None:
        fd, name = tempfile.mkstemp(prefix=config.name + '.dacli-backup-', dir=config.parent)
        backup = Path(name)
        with os.fdopen(fd, 'wb') as stream:
            stream.write(old)
            stream.flush()
            os.fsync(stream.fileno())
    fd, name = tempfile.mkstemp(prefix='.' + config.name + '.dacli-', dir=config.parent)
    staged = Path(name)
    try:
        with os.fdopen(fd, 'wb') as stream:
            stream.write(updated)
            stream.flush()
            os.fsync(stream.fileno())
        current = read(config) if config.exists() or config.is_symlink() else None
        if current != old:
            raise ValueError('configuration changed during preparation; refusing overwrite')
        os.replace(staged, config)
        directory = os.open(config.parent, os.O_RDONLY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if staged.exists():
            staged.unlink()
    return str(backup) if backup else None


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--package', required=True)
    parser.add_argument('--selection', required=True)
    parser.add_argument('--config', required=True)
    parser.add_argument('--install', action='store_true')
    args = parser.parse_args()
    try:
        root, selection, config = (Path(value) for value in (args.package, args.selection, args.config))
        if not all(path.is_absolute() for path in (root, selection, config)):
            raise ValueError('explicit absolute paths required')
        if root.is_symlink() or config.parent.is_symlink():
            raise ValueError('symlinked package/configuration parent unsupported')
        old, updated = render(root, selection, config)
        backup = atomic_install(config, old, updated) if args.install else None
        print(json.dumps({'installed': args.install, 'configuration': json.loads(updated),
                          'backup': backup, 'created_config': old is None,
                          'host_launched': False, 'host_validated': False}, indent=2))
    except (OSError, ValueError, KeyError, TypeError, UnicodeError, RecursionError) as error:
        parser.exit(3, 'dacli adapter installation refused: ' + str(error) + '\n')
