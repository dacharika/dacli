#!/usr/bin/env python3
"""Export a deterministic private preview, excluding probes and repo state."""
from pathlib import Path
import sys
import zipfile

FILES = ('plugin.json', 'mcp.json', '.mcp.json', '.codex-plugin/plugin.json',
         'LICENSE', 'README.md', 'skills/governance-workflow/SKILL.md', 'scripts/serve.py',
         'receipts/install-0.159.2.json')


def build(output, root=None):
    root = Path(root or Path(__file__).resolve().parents[1]).resolve()
    output = Path(output).resolve()
    if output == root or root in output.parents:
        raise ValueError('archive must be outside plugin root')
    payloads = []
    for name in FILES:
        path = root / name
        if path.is_symlink() or not path.is_file():
            raise ValueError('package requires regular file: ' + name)
        payloads.append((name, path.read_bytes()))
    # Read/validate before opening output, so a missing source cannot leave a
    # misleading partial archive. Fixed metadata makes repeated builds equal.
    with zipfile.ZipFile(output, 'w', compression=zipfile.ZIP_DEFLATED) as archive:
        for name, payload in payloads:
            info = zipfile.ZipInfo('dacli-governance/' + name, (2026, 1, 1, 0, 0, 0))
            info.compress_type = zipfile.ZIP_DEFLATED
            info.external_attr = 0o100644 << 16
            archive.writestr(info, payload)
    return output


if __name__ == '__main__':
    if len(sys.argv) != 2:
        sys.exit('usage: package.py /absolute/output/dacli-governance.zip')
    print(build(sys.argv[1]))
