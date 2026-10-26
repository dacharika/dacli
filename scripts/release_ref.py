#!/usr/bin/env python3
"""Resolve an exact release tag to a commit through GitHub's Git API.

Used before target checkout and again before release work and publication.
This deliberately accepts only an existing vX.Y.Z-style tag and a full
expected commit SHA; dispatching the workflow is not authority to publish an
arbitrary revision.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
import urllib.error
import urllib.parse
import urllib.request


TAG_RE = re.compile(
    r"^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)"
    r"(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?"
    r"(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$"
)
SHA_RE = re.compile(r"^[0-9a-fA-F]{40}$")
MAX_TAG_DEPTH = 16


class ReleaseRefError(ValueError):
    """The selected tag is absent, malformed, or no longer matches its SHA."""


def validate_workflow_context(workflow_ref: str, default_branch: str, repository: str) -> None:
    """Require the workflow definition to come from this repository's default branch."""
    if not default_branch or "/" in default_branch or "@" in default_branch:
        raise ReleaseRefError("default branch is invalid")
    expected_prefix = repository + "/.github/workflows/release.yml@"
    if not repository or not workflow_ref.startswith(expected_prefix):
        raise ReleaseRefError("release workflow must run from the repository's trusted workflow file")
    if workflow_ref[len(expected_prefix) :] != "refs/heads/" + default_branch:
        raise ReleaseRefError("release workflow must be dispatched from the default branch")


def validate_inputs(tag: str, expected_commit: str, confirmation: str) -> tuple[str, str]:
    if not TAG_RE.fullmatch(tag):
        raise ReleaseRefError("tag must be a release version such as v1.2.3")
    if not SHA_RE.fullmatch(expected_commit):
        raise ReleaseRefError("expected commit must be a full 40-character SHA")
    if confirmation != tag:
        raise ReleaseRefError("confirmation must exactly match the selected tag")
    return tag, expected_commit.lower()


def _get_json(url: str, token: str, opener=urllib.request.urlopen) -> dict:
    request = urllib.request.Request(
        url,
        headers={
            "Accept": "application/vnd.github+json",
            "Authorization": "Bearer " + token,
            "User-Agent": "dacli-release-ref-check",
            "X-GitHub-Api-Version": "2022-11-28",
        },
    )
    try:
        with opener(request, timeout=20) as response:
            value = json.loads(response.read())
    except (urllib.error.HTTPError, urllib.error.URLError, TimeoutError, json.JSONDecodeError) as exc:
        if isinstance(exc, urllib.error.HTTPError):
            exc.close()
        raise ReleaseRefError("GitHub could not confirm the selected tag") from exc
    if not isinstance(value, dict):
        raise ReleaseRefError("GitHub returned an invalid tag response")
    return value


def resolve_tag(
    api_url: str, repository: str, tag: str, token: str, opener=urllib.request.urlopen
) -> tuple[str, str]:
    """Return the tag ref object SHA and peeled commit SHA."""
    if not TAG_RE.fullmatch(tag):
        raise ReleaseRefError("tag must be a release version such as v1.2.3")
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", repository):
        raise ReleaseRefError("repository is invalid")
    base = api_url.rstrip("/")
    encoded_tag = urllib.parse.quote(tag, safe="")
    ref = _get_json(
        f"{base}/repos/{repository}/git/ref/tags/{encoded_tag}", token, opener
    )
    obj = ref.get("object")
    if not isinstance(obj, dict):
        raise ReleaseRefError("GitHub tag ref has no target object")
    ref_sha = obj.get("sha")
    if not isinstance(ref_sha, str) or not SHA_RE.fullmatch(ref_sha):
        raise ReleaseRefError("GitHub tag ref has an invalid target SHA")
    ref_sha = ref_sha.lower()
    seen: set[str] = set()
    for _ in range(MAX_TAG_DEPTH):
        kind, sha = obj.get("type"), obj.get("sha")
        if kind not in {"tag", "commit"} or not isinstance(sha, str) or not SHA_RE.fullmatch(sha):
            raise ReleaseRefError("release tag does not resolve to a commit")
        sha = sha.lower()
        if kind == "commit":
            return ref_sha, sha
        if sha in seen:
            raise ReleaseRefError("annotated tag chain contains a cycle")
        seen.add(sha)
        tag_object = _get_json(f"{base}/repos/{repository}/git/tags/{sha}", token, opener)
        obj = tag_object.get("object")
        if not isinstance(obj, dict):
            raise ReleaseRefError("annotated tag has no target object")
    raise ReleaseRefError("annotated tag chain is too deep")


def check_ref(
    api_url: str,
    repository: str,
    tag: str,
    expected_commit: str,
    token: str,
    expected_tag_object: str | None = None,
    opener=urllib.request.urlopen,
) -> tuple[str, str]:
    tag_object, actual = resolve_tag(api_url, repository, tag, token, opener)
    if actual != expected_commit.lower():
        raise ReleaseRefError("selected tag no longer resolves to the expected commit")
    if expected_tag_object is not None and (
        not SHA_RE.fullmatch(expected_tag_object)
        or tag_object != expected_tag_object.lower()
    ):
        raise ReleaseRefError("selected tag ref object moved after validation")
    ensure_release_absent(api_url, repository, tag, token, opener)
    return tag_object, actual


def ensure_release_absent(
    api_url: str, repository: str, tag: str, token: str, opener=urllib.request.urlopen
) -> None:
    """Refuse retries when a GitHub Release already exists for this tag."""
    url = f"{api_url.rstrip('/')}/repos/{repository}/releases/tags/{urllib.parse.quote(tag, safe='')}"
    request = urllib.request.Request(
        url,
        headers={
            "Accept": "application/vnd.github+json",
            "Authorization": "Bearer " + token,
            "User-Agent": "dacli-release-ref-check",
            "X-GitHub-Api-Version": "2022-11-28",
        },
    )
    try:
        with opener(request, timeout=20):
            raise ReleaseRefError("a GitHub Release already exists for this tag; inspect it before proceeding")
    except urllib.error.HTTPError as exc:
        if exc.code == 404:
            exc.close()
            return
        exc.close()
        raise ReleaseRefError("GitHub could not confirm release state") from exc
    except (urllib.error.URLError, TimeoutError) as exc:
        raise ReleaseRefError("GitHub could not confirm release state") from exc


def main(argv: list[str] | None = None, opener=urllib.request.urlopen) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tag", required=True)
    parser.add_argument("--expected-commit", required=True)
    parser.add_argument("--expected-tag-object")
    parser.add_argument("--confirmation", required=True)
    parser.add_argument("--workflow-ref", required=True)
    parser.add_argument("--default-branch", required=True)
    parser.add_argument("--repository", required=True)
    parser.add_argument("--api-url", default=os.environ.get("GITHUB_API_URL", "https://api.github.com"))
    parser.add_argument("--token", default=os.environ.get("GITHUB_TOKEN"))
    parser.add_argument("--output", default=os.environ.get("GITHUB_OUTPUT"))
    args = parser.parse_args(argv)
    try:
        validate_workflow_context(args.workflow_ref, args.default_branch, args.repository)
        tag, expected = validate_inputs(args.tag, args.expected_commit, args.confirmation)
        if not args.token:
            raise ReleaseRefError("GitHub API token is required")
        tag_object, commit = check_ref(
            args.api_url,
            args.repository,
            tag,
            expected,
            args.token,
            expected_tag_object=args.expected_tag_object,
            opener=opener,
        )
        if args.output:
            with open(args.output, "a", encoding="utf-8") as output:
                output.write(f"tag={tag}\ncommit={commit}\ntag_object={tag_object}\n")
        else:
            print(json.dumps({"tag": tag, "commit": commit, "tag_object": tag_object}))
        return 0
    except (ReleaseRefError, OSError) as exc:
        print(f"release ref validation failed: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
