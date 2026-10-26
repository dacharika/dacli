#!/usr/bin/env python3
"""Behavioral tests for release_ref.py against a local fake GitHub API."""

from __future__ import annotations

import json
import io
import pathlib
import sys
import tempfile
import urllib.error
import urllib.parse
import unittest
from contextlib import redirect_stderr

sys.path.insert(0, str(pathlib.Path(__file__).resolve().parent))
import release_ref


COMMIT = "1" * 40
OTHER_COMMIT = "2" * 40
TAG_OBJECT = "a" * 40
INNER_TAG_OBJECT = "b" * 40


class FakeGitHub:
    def __init__(self):
        self.responses: dict[str, tuple[int, dict]] = {}
        self.api_url = "https://api.example.test"

    def close(self):
        pass

    def __call__(self, request, timeout):
        self.assert_auth(request, timeout)
        path = urllib.parse.urlsplit(request.full_url).path
        status, body = self.responses.get(path, (404, {"message": "Not Found"}))
        if status != 200:
            raise urllib.error.HTTPError(request.full_url, status, "Not Found", {}, None)
        return io.BytesIO(json.dumps(body).encode())

    @staticmethod
    def assert_auth(request, timeout):
        if timeout != 20 or request.get_header("Authorization") != "Bearer test-token":
            raise AssertionError("fake API request omitted bounded authenticated headers")

    def set_ref(self, obj: dict):
        self.responses["/repos/dacharika/dacli/git/ref/tags/v1.2.3"] = (200, {"object": obj})


class ReleaseRefTests(unittest.TestCase):
    def setUp(self):
        self.github = FakeGitHub()

    def tearDown(self):
        self.github.close()

    def resolve(self, expected=COMMIT):
        return release_ref.check_ref(
            self.github.api_url,
            "dacharika/dacli",
            "v1.2.3",
            expected,
            "test-token",
            opener=self.github,
        )

    def test_resolves_lightweight_tag(self):
        self.github.set_ref({"type": "commit", "sha": COMMIT})
        self.assertEqual(self.resolve(), (COMMIT, COMMIT))

    def test_peels_nested_annotated_tags(self):
        self.github.set_ref({"type": "tag", "sha": TAG_OBJECT})
        self.github.responses[f"/repos/dacharika/dacli/git/tags/{TAG_OBJECT}"] = (
            200,
            {"object": {"type": "tag", "sha": INNER_TAG_OBJECT}},
        )
        self.github.responses[f"/repos/dacharika/dacli/git/tags/{INNER_TAG_OBJECT}"] = (
            200,
            {"object": {"type": "commit", "sha": COMMIT}},
        )
        self.assertEqual(self.resolve(), (TAG_OBJECT, COMMIT))

    def test_rejects_moved_tag(self):
        self.github.set_ref({"type": "commit", "sha": OTHER_COMMIT})
        with self.assertRaisesRegex(release_ref.ReleaseRefError, "expected commit"):
            self.resolve()

    def test_rejects_replaced_tag_object_even_if_commit_is_unchanged(self):
        self.github.set_ref({"type": "tag", "sha": TAG_OBJECT})
        self.github.responses[f"/repos/dacharika/dacli/git/tags/{TAG_OBJECT}"] = (
            200,
            {"object": {"type": "commit", "sha": COMMIT}},
        )
        with self.assertRaisesRegex(release_ref.ReleaseRefError, "ref object moved"):
            release_ref.check_ref(
                self.github.api_url,
                "dacharika/dacli",
                "v1.2.3",
                COMMIT,
                "test-token",
                expected_tag_object=INNER_TAG_OBJECT,
                opener=self.github,
            )

    def test_refuses_repeat_when_release_already_exists(self):
        self.github.set_ref({"type": "commit", "sha": COMMIT})
        self.github.responses["/repos/dacharika/dacli/releases/tags/v1.2.3"] = (200, {"id": 17})
        with self.assertRaisesRegex(release_ref.ReleaseRefError, "Release already exists"):
            self.resolve()

    def test_rejects_deleted_tag(self):
        with self.assertRaisesRegex(release_ref.ReleaseRefError, "could not confirm"):
            self.resolve()

    def test_rejects_tag_to_non_commit(self):
        self.github.set_ref({"type": "tree", "sha": COMMIT})
        with self.assertRaisesRegex(release_ref.ReleaseRefError, "does not resolve to a commit"):
            self.resolve()

    def test_rejects_annotated_tag_cycle(self):
        self.github.set_ref({"type": "tag", "sha": TAG_OBJECT})
        self.github.responses[f"/repos/dacharika/dacli/git/tags/{TAG_OBJECT}"] = (
            200,
            {"object": {"type": "tag", "sha": TAG_OBJECT}},
        )
        with self.assertRaisesRegex(release_ref.ReleaseRefError, "cycle"):
            self.resolve()

    def test_rejects_bad_version_sha_and_confirmation(self):
        with self.assertRaisesRegex(release_ref.ReleaseRefError, "release version"):
            release_ref.validate_inputs("main", COMMIT, "main")
        with self.assertRaisesRegex(release_ref.ReleaseRefError, "full 40-character"):
            release_ref.validate_inputs("v1.2.3", "abc", "v1.2.3")
        with self.assertRaisesRegex(release_ref.ReleaseRefError, "confirmation"):
            release_ref.validate_inputs("v1.2.3", COMMIT, "v1.2.4")

    def test_requires_trusted_default_branch_workflow_revision(self):
        release_ref.validate_workflow_context(
            "dacharika/dacli/.github/workflows/release.yml@refs/heads/main",
            "main",
            "dacharika/dacli",
        )
        for workflow_ref in (
            "dacharika/dacli/.github/workflows/release.yml@refs/heads/feature",
            "attacker/dacli/.github/workflows/release.yml@refs/heads/main",
        ):
            with self.subTest(workflow_ref=workflow_ref):
                with self.assertRaises(release_ref.ReleaseRefError):
                    release_ref.validate_workflow_context(workflow_ref, "main", "dacharika/dacli")

    def test_cli_resolves_and_writes_validated_tag_commit_and_ref_object(self):
        self.github.set_ref({"type": "commit", "sha": COMMIT})
        with tempfile.NamedTemporaryFile(mode="r+", encoding="utf-8") as output:
            result = release_ref.main(
                [
                    "--tag", "v1.2.3",
                    "--expected-commit", COMMIT,
                    "--confirmation", "v1.2.3",
                    "--workflow-ref", "dacharika/dacli/.github/workflows/release.yml@refs/heads/main",
                    "--default-branch", "main",
                    "--repository", "dacharika/dacli",
                    "--token", "test-token",
                    "--api-url", self.github.api_url,
                    "--output", output.name,
                ],
                opener=self.github,
            )
            output.seek(0)
            self.assertEqual(result, 0)
            self.assertEqual(
                output.read(),
                f"tag=v1.2.3\ncommit={COMMIT}\ntag_object={COMMIT}\n",
            )

    def test_cli_fails_before_output_when_tag_ref_object_was_replaced(self):
        self.github.set_ref({"type": "tag", "sha": TAG_OBJECT})
        self.github.responses[f"/repos/dacharika/dacli/git/tags/{TAG_OBJECT}"] = (
            200,
            {"object": {"type": "commit", "sha": COMMIT}},
        )
        with tempfile.NamedTemporaryFile(mode="r+", encoding="utf-8") as output:
            with redirect_stderr(io.StringIO()):
                result = release_ref.main(
                    [
                        "--tag", "v1.2.3",
                        "--expected-commit", COMMIT,
                        "--expected-tag-object", INNER_TAG_OBJECT,
                        "--confirmation", "v1.2.3",
                        "--workflow-ref", "dacharika/dacli/.github/workflows/release.yml@refs/heads/main",
                        "--default-branch", "main",
                        "--repository", "dacharika/dacli",
                        "--token", "test-token",
                        "--api-url", self.github.api_url,
                        "--output", output.name,
                    ],
                    opener=self.github,
                )
            output.seek(0)
            self.assertEqual(result, 1)
            self.assertEqual(output.read(), "")


if __name__ == "__main__":
    unittest.main()
