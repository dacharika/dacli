#!/usr/bin/env python3
"""Shared backend conformance entrypoint; no antigravity certification."""
import runpy
from pathlib import Path

if __name__ == "__main__":
    module = runpy.run_path(str(Path(__file__).resolve().parents[3] / "contracts/governance/v1/conformance.py"))
    raise SystemExit(module["main"]("antigravity"))
