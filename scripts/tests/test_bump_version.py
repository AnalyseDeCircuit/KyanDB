"""Tests for scripts/release/bump_version.py."""

from __future__ import annotations

import json
import tempfile
import unittest
from pathlib import Path

from scripts.release import bump_version


SAMPLE_MANIFEST = """{
  "name": "KyanDB",
  "identifier": "com.analysedecircuit.kyandb",
  "version": "0.1.0",
  "icon": "assets/icon.png",
  "out": "build",
  "i18n": {
    "dir": "locales",
    "source": "en"
  }
}
"""


class BumpVersionTests(unittest.TestCase):
    def test_validate_version_accepts_semver_and_rejects_malformed(self) -> None:
        for valid in ("0.2.0", "1.0.0", "0.2.0-alpha.1", "2.1.3-rc.2"):
            bump_version.validate_version(valid)

        for invalid in ("v0.2.0", "0.2", "01.2.3", "0.2.0beta1", "0.2.0+build", ""):
            with self.subTest(invalid=invalid):
                with self.assertRaises(ValueError):
                    bump_version.validate_version(invalid)

    def test_updated_manifest_replaces_version_and_preserves_layout(self) -> None:
        updated = bump_version.updated_manifest(SAMPLE_MANIFEST, "0.2.0-beta.1")
        self.assertEqual(json.loads(updated)["version"], "0.2.0-beta.1")
        self.assertEqual(
            updated,
            SAMPLE_MANIFEST.replace('"version": "0.1.0"', '"version": "0.2.0-beta.1"'),
        )

    def test_main_dry_run_does_not_modify_manifest(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            manifest = Path(tmp) / "mygo.json"
            manifest.write_text(SAMPLE_MANIFEST, encoding="utf-8")
            rc = bump_version.main(["0.3.0", "--manifest", str(manifest), "--dry-run"])
            self.assertEqual(rc, 0)
            self.assertEqual(manifest.read_text(encoding="utf-8"), SAMPLE_MANIFEST)

    def test_main_writes_new_version(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            manifest = Path(tmp) / "mygo.json"
            manifest.write_text(SAMPLE_MANIFEST, encoding="utf-8")
            rc = bump_version.main(["0.3.0", "--manifest", str(manifest)])
            self.assertEqual(rc, 0)
            self.assertEqual(json.loads(manifest.read_text(encoding="utf-8"))["version"], "0.3.0")


if __name__ == "__main__":
    unittest.main()
