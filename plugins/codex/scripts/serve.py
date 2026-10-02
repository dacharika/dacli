#!/usr/bin/env python3
"""Start the existing MCP server with explicit local connection authority.

This bootstrap never launches agents or grants native governance sessions.
The owner provisions a workspace credential outside the plugin/cache/transcript.
"""
import json
import os
from pathlib import Path
import stat
import sys


def refuse(message):
    print('dacli plugin: ' + message, file=sys.stderr)
    return 3


def launch(connection):
    try:
        config = json.loads(Path(connection).read_text(encoding='utf-8'))
        if not isinstance(config, dict) or set(config) != {'workspace', 'binary', 'credential_file'}:
            return refuse('connection requires workspace, binary and credential_file only')
        if any(not isinstance(value, str) or not value or '\x00' in value for value in config.values()):
            return refuse('connection paths must be nonempty strings')
        workspace = Path(config['workspace'])
        binary = Path(config['binary'])
        credential = Path(config['credential_file'])
        if any(not path.is_absolute() for path in (workspace, binary, credential)):
            return refuse('connection paths must be absolute')
        if not workspace.is_dir() or not (workspace / '.dacli').is_dir():
            return refuse('configured workspace must already contain .dacli')
        if not binary.is_file() or not os.access(binary, os.X_OK):
            return refuse('configured dacli binary must be executable')
        # Open once, without following a credential symlink, then validate that
        # exact file descriptor before reading. A path stat/read pair races.
        fd = os.open(credential, os.O_RDONLY | os.O_NONBLOCK | os.O_NOFOLLOW)
        with os.fdopen(fd, 'rb') as stream:
            info = os.fstat(stream.fileno())
            if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077:
                return refuse('credential must be an owner-only regular file')
            raw = stream.read(4097)
        if len(raw) > 4096:
            return refuse('credential file is too large')
        token = raw.decode('utf-8').rstrip('\r\n')
        if not token or any(ch.isspace() or ord(ch) < 32 for ch in token):
            return refuse('credential must contain one nonempty token')
        env = dict(os.environ)
        # Override ambient identity explicitly; never pass credentials in argv.
        env['DACLI_AGENT'] = token
        os.chdir(workspace)
        os.execve(str(binary), [str(binary), 'mcp', 'serve'], env)
    except (OSError, ValueError, UnicodeError):
        # Exceptions can contain file content; do not print them.
        return refuse('connection unavailable or invalid; provision ' + str(connection))


if __name__ == '__main__':
    if len(sys.argv) > 2:
        sys.exit(refuse('expected at most one connection file'))
    connection = sys.argv[1] if len(sys.argv) == 2 else Path.home() / '.config/dacli/plugin/connection.json'
    sys.exit(launch(connection))
