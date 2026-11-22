"""Tests for scripts/quality/audit_i18n.py."""

from __future__ import annotations

import json
import tempfile
import unittest
from pathlib import Path

from scripts.quality import audit_i18n


def write_sample_project(
    root: Path,
    *,
    en_raw: str | None = None,
    zh_raw: str | None = None,
    gen_go: str | None = None,
    ui_go: str | None = None,
) -> tuple[Path, Path]:
    locale_dir = root / "locales"
    ui_dir = root / "internal" / "ui" / "app"
    locale_dir.mkdir(parents=True)
    ui_dir.mkdir(parents=True)

    if en_raw is None:
        en_raw = json.dumps(
            {
                "@@locale": "en",
                "queryRun": "Run",
                "@queryRun": {"description": "Run SQL button"},
                "plateVia": "via {host}",
                "@plateVia": {
                    "description": "Jump host route",
                    "placeholders": {"host": {"type": "String"}},
                },
                "ledgerPending": "{count, plural, one {# pending} other {# pending}}",
                "@ledgerPending": {
                    "description": "Pending edit count",
                    "placeholders": {"count": {"type": "int"}},
                },
            },
            indent=2,
        )
    if zh_raw is None:
        zh_raw = json.dumps(
            {
                "@@locale": "zh-Hans",
                "queryRun": "运行",
                "plateVia": "经由 {host}",
                "ledgerPending": "{count, plural, other {# 项待提交}}",
            },
            ensure_ascii=False,
            indent=2,
        )
    if gen_go is None:
        gen_go = """package locales
func (msgs Messages) QueryRun() string {
\treturn Catalog.Format(msgs.locale, "queryRun", nil)
}
func (msgs Messages) PlateVia(host string) string {
\treturn Catalog.Format(msgs.locale, "plateVia", nil)
}
func (msgs Messages) LedgerPending(count int) string {
\treturn Catalog.Format(msgs.locale, "ledgerPending", nil)
}
"""
    if ui_go is None:
        ui_go = """package app
import "github.com/AnalyseDeCircuit/kyandb/locales"
func view(c any) {
\tm := locales.In("en")
\t_ = m.QueryRun()
\t_ = m.PlateVia("bastion")
\t_ = m.LedgerPending(2)
}
"""

    (locale_dir / "en.json").write_text(en_raw, encoding="utf-8")
    (locale_dir / "zh-Hans.json").write_text(zh_raw, encoding="utf-8")
    (locale_dir / "catalog_gen.go").write_text(gen_go, encoding="utf-8")
    (ui_dir / "view.go").write_text(ui_go, encoding="utf-8")
    return locale_dir, root / "internal" / "ui"


class AuditI18nTests(unittest.TestCase):
    def test_extract_icu_placeholders_handles_nested_and_escaped(self) -> None:
        self.assertEqual(
            audit_i18n.extract_icu_placeholders(
                "{n, plural, offset:1 =0 {Nobody} =1 {{host}} other {{host} and # others}}"
            ),
            {"n", "host"},
        )
        self.assertEqual(
            audit_i18n.extract_icu_placeholders("Don't quote '{ignored}' but keep {real}"),
            {"real"},
        )
        with self.assertRaises(ValueError):
            audit_i18n.extract_icu_placeholders("{count, plural, one {# item}}")

    def test_repo_catalogs_pass_audit(self) -> None:
        locale_dir, source_locale = audit_i18n.resolve_i18n_config(
            audit_i18n.DEFAULT_CONFIG, None, None
        )
        res = audit_i18n.audit(locale_dir, source_locale, audit_i18n.DEFAULT_UI_ROOT)
        self.assertFalse(res.has_errors())
        self.assertEqual(res.unused_keys, [])
        self.assertEqual(res.direct_locale_calls, [])

    def test_detects_missing_extra_duplicate_and_placeholder_drift(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            zh_raw = """{
  "@@locale": "zh-Hans",
  "queryRun": "运行",
  "queryRun": "执行",
  "plateVia": "经由 {wrongHost}",
  "extraKey": "多余"
}"""
            locale_dir, ui_root = write_sample_project(root, zh_raw=zh_raw)
            res = audit_i18n.audit(locale_dir, "en", ui_root)
            self.assertTrue(res.has_errors())
            self.assertEqual(res.catalogs["zh-Hans"].duplicates, ["queryRun"])
            self.assertEqual(res.missing_by_locale, {"zh-Hans": ["ledgerPending"]})
            self.assertEqual(res.extra_by_locale, {"zh-Hans": ["extraKey"]})
            self.assertEqual(
                res.placeholder_mismatches,
                {"plateVia": {"en": ["host"], "zh-Hans": ["wrongHost"]}},
            )

    def test_detects_missing_source_metadata_and_stale_gen(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            en_raw = json.dumps(
                {
                    "@@locale": "en",
                    "queryRun": "Run",
                    "plateVia": "via {host}",
                    "@plateVia": {"description": "Jump host"},
                    "ledgerPending": "{count, plural, one {# pending} other {# pending}}",
                    "@ledgerPending": {"description": "Pending count"},
                }
            )
            gen_go = """package locales
func (msgs Messages) QueryRun() string {
\treturn Catalog.Format(msgs.locale, "queryRun", nil)
}
func (msgs Messages) ObsoleteKey() string {
\treturn Catalog.Format(msgs.locale, "obsoleteKey", nil)
}
"""
            locale_dir, ui_root = write_sample_project(root, en_raw=en_raw, gen_go=gen_go)
            res = audit_i18n.audit(locale_dir, "en", ui_root)
            self.assertTrue(res.has_errors())
            self.assertEqual(res.missing_source_metadata, ["queryRun"])
            self.assertEqual(res.gen_missing_keys, ["ledgerPending", "plateVia"])
            self.assertEqual(res.gen_stale_keys, ["obsoleteKey"])

    def test_detects_direct_global_locale_calls_but_ignores_comments(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            ui_go = """package app
import "github.com/AnalyseDeCircuit/kyandb/locales"
// locales.QueryRun() in a comment must be ignored.
func view() {
\tm := locales.In("en")
\t_ = m.PlateVia("bastion")
\t_ = m.LedgerPending(1)
\t_ = locales.QueryRun()
}
"""
            locale_dir, ui_root = write_sample_project(root, ui_go=ui_go)
            res = audit_i18n.audit(locale_dir, "en", ui_root)
            self.assertTrue(res.has_errors())
            self.assertEqual(len(res.direct_locale_calls), 1)
            self.assertIn("locales.QueryRun()", res.direct_locale_calls[0])

    def test_detects_english_copy_and_unused_key(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            zh_raw = json.dumps(
                {
                    "@@locale": "zh-Hans",
                    "queryRun": "运行",
                    "plateVia": "via {host}",
                    "ledgerPending": "{count, plural, other {# 项待提交}}",
                },
                ensure_ascii=False,
            )
            ui_go = """package app
import "github.com/AnalyseDeCircuit/kyandb/locales"
func view() {
\tm := locales.In("en")
\t_ = m.QueryRun()
}
"""
            locale_dir, ui_root = write_sample_project(root, zh_raw=zh_raw, ui_go=ui_go)
            res = audit_i18n.audit(locale_dir, "en", ui_root)
            self.assertFalse(res.has_errors())
            self.assertTrue(res.has_errors(fail_on_english_copy=True))
            self.assertTrue(res.has_errors(fail_on_unused=True))
            self.assertEqual(res.english_copies, {"zh-Hans": ["plateVia"]})
            self.assertEqual(res.unused_keys, ["ledgerPending", "plateVia"])


if __name__ == "__main__":
    unittest.main()
