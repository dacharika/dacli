"""Check current dacli skill examples against its capability requirements."""
from pathlib import Path
import json
import re
import shlex


REMOVED_EXAMPLE = re.compile(r"^\s*dacli\s+(?:start\s+--profile|loop\b|service\b|spawn\b)")


def command_id(path):
    return "cli.command." + ".".join(
        part.replace("_", "-").lower() for part in path.replace("/", " ").split()
    )


def code_examples(markdown):
    in_fence = False
    lines = []
    for line in markdown.splitlines():
        if line.lstrip().startswith("```"):
            if in_fence:
                yield from _commands(lines)
                lines = []
            in_fence = not in_fence
        elif in_fence:
            lines.append(line)


def _commands(lines):
    current = ""
    for line in lines:
        stripped = line.strip()
        if current:
            current += " " + stripped.rstrip("\\").strip()
            if stripped.endswith("\\"):
                continue
        elif stripped.startswith("dacli "):
            current = stripped.rstrip("\\").strip()
            if stripped.endswith("\\"):
                continue
        else:
            continue
        if current:
            if REMOVED_EXAMPLE.match(current):
                yield (current, None, "removed launcher example")
            else:
                try:
                    tokens = shlex.split(current)
                except ValueError:
                    yield (current, None, "malformed shell example")
                else:
                    yield (current, tokens, "")
            current = ""


def validate_skill(root, skill_override=None, runtime_manifest_path=None):
    root = Path(root)
    skill = root / "skills/dacli"
    requirements = json.loads((skill / "capabilities.json").read_text())
    if requirements.get("schema_version") != 1:
        return ["unsupported skill requirements schema"]
    declared = {entry["id"] for key in ("required", "optional") for entry in requirements.get(key, [])}
    runtime = None
    if runtime_manifest_path is not None:
        runtime = json.loads(Path(runtime_manifest_path).read_text())
        if runtime.get("schema_version") != 1:
            return ["unsupported runtime capability manifest schema"]
        runtime_commands = {entry["path"]: entry for entry in runtime.get("commands", [])}
    else:
        runtime_commands = {}
    paths = [skill / "SKILL.md", *sorted((skill / "references").glob("*.md"))]
    errors = []
    for path in paths:
        markdown = skill_override if path.name == "SKILL.md" and skill_override is not None else path.read_text()
        for example, tokens, error in code_examples(markdown):
            if error:
                errors.append(f"{path.relative_to(root)}: {error}: {example}")
                continue
            if not tokens or tokens[0] != "dacli":
                errors.append(f"{path.relative_to(root)}: malformed command: {example}")
                continue
            words = []
            for token in tokens[1:]:
                if token.startswith("-") or token.startswith("<"):
                    break
                words.append(token)
            matched = None
            for length in range(len(words), 0, -1):
                candidate = " ".join(words[:length])
                if command_id(candidate) in declared:
                    matched = (candidate, length)
                    break
            if not matched:
                errors.append(f"{path.relative_to(root)}: command absent from capability requirements: {example}")
                continue
            cmd, length = matched
            cmd_capability = command_id(cmd)
            runtime_command = runtime_commands.get(cmd)
            if runtime is not None and runtime_command is None:
                errors.append(f"{path.relative_to(root)}: {cmd} absent from selected runtime capability manifest")
                continue
            tail = tokens[1 + length:]
            for token in tail:
                if token.startswith("--"):
                    flag = token[2:].split("=", 1)[0]
                    # --json is a dispatcher option: its contract is the
                    # command's JSON capability, not necessarily a usage flag.
                    if flag == "json":
                        if runtime is not None and not runtime_command.get("json", False):
                            errors.append(f"{path.relative_to(root)}: {cmd} does not advertise JSON output")
                    elif cmd_capability + ".flag." + flag not in declared:
                        errors.append(f"{path.relative_to(root)}: --{flag} absent from capability requirements for {cmd}")
                    if runtime is not None and flag != "json" and not any(
                            item.get("name") == "--" + flag for item in runtime_command.get("flags", [])):
                        errors.append(f"{path.relative_to(root)}: --{flag} absent from selected runtime capability manifest for {cmd}")
    return errors


if __name__ == "__main__":
    import argparse
    parser = argparse.ArgumentParser()
    parser.add_argument("root", type=Path)
    errors = validate_skill(parser.parse_args().root)
    if errors:
        raise SystemExit("\n".join(errors))
    print("skill command examples match the declared capability requirements")
