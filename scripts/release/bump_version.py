#!/usr/bin/env python3
"""Bump the KyanDB application version in mygo.json."""

from __future__ import annotations

import argparse
import json
import re
import sys
from pathlib import Path


ROOT_DIR = Path(__file__).resolve().parents[2]
APP_MANIFEST = ROOT_DIR / "mygo.json"
SEMVER_RE = re.compile(
    r"^(0|[1-9]\d*)\."
    r"(0|[1-9]\d*)\."
    r"(0|[1-9]\d*)"
    r"(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$"
)


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Update the KyanDB SemVer version in mygo.json.",
    )
    parser.add_argument(
        "version",
        help="New SemVer version, for example 0.2.0 or 0.2.0-beta.1.",
    )
    parser.add_argument(
        "--manifest",
        type=Path,
        default=APP_MANIFEST,
        help="Path to mygo.json (default: repository root).",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="Print the planned change without writing files.",
    )
    return parser.parse_args(argv)


def validate_version(version: str) -> None:
    if SEMVER_RE.fullmatch(version):
        return
    raise ValueError(
        f"invalid version {version!r}; use SemVer like 0.2.0 or 0.2.0-beta.1"
    )


def current_version(manifest_text: str) -> str:
    data = json.loads(manifest_text)
    version = data.get("version")
    if not isinstance(version, str) or not version:
        raise RuntimeError('"version" field not found in mygo.json')
    return version


def updated_manifest(manifest_text: str, new_version: str) -> str:
    # Validate JSON structure first so a corrupt file fails loudly.
    current_version(manifest_text)
    updated, count = re.subn(
        r'("version"\s*:\s*)"[^"]*"',
        rf'\g<1>"{new_version}"',
        manifest_text,
        count=1,
    )
    if count != 1:
        raise RuntimeError('"version" field not found in mygo.json')
    # Verify the rewritten text still parses cleanly with the expected value.
    if json.loads(updated).get("version") != new_version:
        raise RuntimeError("failed to update version cleanly in mygo.json")
    return updated


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    try:
        validate_version(args.version)
        manifest_text = args.manifest.read_text(encoding="utf-8")
        old_version = current_version(manifest_text)
        new_manifest_text = updated_manifest(manifest_text, args.version)
    except Exception as error:
        print(f"error: {error}", file=sys.stderr)
        return 1

    print(f"KyanDB version: {old_version} -> {args.version}")
    if args.dry_run:
        print("dry run: mygo.json was not changed")
        return 0

    args.manifest.write_text(new_manifest_text, encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
