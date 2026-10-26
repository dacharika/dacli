import importlib.util
from pathlib import Path
import unittest

ROOT = Path(__file__).resolve().parents[3]
SPEC = importlib.util.spec_from_file_location(
    'validate_skill_examples', ROOT / 'plugins/codex/scripts/validate_skill_examples.py')
VALIDATOR = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(VALIDATOR)


class SkillContractTests(unittest.TestCase):
    def test_current_examples_are_declared_and_do_not_recommend_removed_launchers(self):
        runtime = ROOT / 'plugins/codex/tests/fixtures/capabilities-source-0199f0d1.json'
        errors = VALIDATOR.validate_skill(ROOT, runtime_manifest_path=runtime)
        self.assertEqual(errors, [], '\n'.join(errors))

    def test_validator_rejects_removed_launcher_and_undeclared_flag(self):
        original = (ROOT / 'skills/dacli/SKILL.md').read_text()
        modified = original.replace('dacli capabilities --json',
                                    'dacli start --profile task --secret')
        modified += '\n```sh\ndacli capabilities --secret\n```\n'
        runtime = ROOT / 'plugins/codex/tests/fixtures/capabilities-source-0199f0d1.json'
        errors = VALIDATOR.validate_skill(ROOT, skill_override=modified,
                                          runtime_manifest_path=runtime)
        self.assertTrue(any('removed launcher example' in item for item in errors), errors)
        self.assertTrue(any('--secret absent' in item for item in errors), errors)


if __name__ == '__main__':
    unittest.main()
