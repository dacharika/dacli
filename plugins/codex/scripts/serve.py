#!/usr/bin/env python3
"""Select an explicit native session profile; never enroll or recover authority."""
import json
import os
from pathlib import Path
import stat
import sys

MAX_CONFIG_BYTES = 16384


def refuse(message):
    print('dacli plugin: ' + message, file=sys.stderr)
    return 3


def selection(path):
    # Validate the opened inode, not a stat/read pair. The owner must also
    # protect parent directories; this is not hostile-process containment.
    fd = os.open(path, os.O_RDONLY | os.O_NONBLOCK | os.O_NOFOLLOW)
    with os.fdopen(fd, 'rb') as stream:
        info = os.fstat(stream.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077:
            raise ValueError('private regular config required')
        raw = stream.read(MAX_CONFIG_BYTES + 1)
    if len(raw) > MAX_CONFIG_BYTES:
        raise ValueError('oversized config')
    def unique(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise ValueError('duplicate key')
            result[key] = value
        return result
    config = json.loads(raw, object_pairs_hook=unique)
    required = {'schema_version', 'binary', 'connection_file'}
    optional = {'runner_config_file', 'review_config_file', 'orchestration_config_file'}
    if not isinstance(config, dict) or not required <= set(config) or set(config) - required - optional:
        raise ValueError('unknown config')
    if type(config['schema_version']) is not int or config['schema_version'] != 1:
        raise ValueError('unknown version')
    if len(optional & set(config)) > 1:
        raise ValueError('separate runner, reviewer and orchestration connections required')
    for key in ('binary', 'connection_file', *(key for key in optional if key in config)):
        value = config[key]
        if not isinstance(value, str) or not value or '\x00' in value or not Path(value).is_absolute():
            raise ValueError('absolute paths required')
    return config


def launch(connection):
    try:
        config = selection(connection)
        binary = config['binary']
        if not Path(binary).is_file() or not os.access(binary, os.X_OK):
            raise ValueError('binary unavailable')
        env = dict(os.environ)
        env.pop('DACLI_AGENT', None)
        # The native server validates its own private profile and credential on
        # startup and reauthenticates each request. This adapter never reads tokens.
        args = [binary, 'native', 'serve', '--connection', config['connection_file']]
        if 'runner_config_file' in config:
            args.extend(['--runner-config', config['runner_config_file']])
        if 'review_config_file' in config:
            args.extend(['--review-config', config['review_config_file']])
        if 'orchestration_config_file' in config:
            args.extend(['--orchestration-config', config['orchestration_config_file']])
        os.execve(binary, args, env)
    except (OSError, ValueError, UnicodeError, RecursionError):
        return refuse('private native connection selection unavailable or invalid')


if __name__ == '__main__':
    if len(sys.argv) > 2:
        sys.exit(refuse('expected at most one selection file'))
    connection = sys.argv[1] if len(sys.argv) == 2 else Path.home() / '.config/dacli/plugin/connection.json'
    sys.exit(launch(connection))
