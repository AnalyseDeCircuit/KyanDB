#!/usr/bin/env python3
"""Audit KyanDB's mygo/i18n ARB catalogs, generated accessors, and Go UI usage."""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any


ROOT_DIR = Path(__file__).resolve().parents[2]
DEFAULT_CONFIG = ROOT_DIR / "mygo.json"
DEFAULT_UI_ROOT = ROOT_DIR / "internal" / "ui"

# Matches generated accessor bodies in locales/catalog_gen.go:
#   func (msgs Messages) ToolbarRefresh() string {
#       return Catalog.Format(msgs.locale, "toolbarRefresh", nil)
#   }
GENERATED_METHOD_RE = re.compile(
    r'func\s*\(\w+\s+Messages\)\s+([A-Z][A-Za-z0-9_]*)\([^)]*\)\s+string\s*\{\s*'
    r'return\s+Catalog\.Format\(\w+\.locale,\s*"([^"]+)"',
    re.MULTILINE,
)

# Direct calls to process-global accessors such as locales.QueryRun() bypass
# the window locale (c.Locale()). Views must use locales.In(c.Locale()).
DIRECT_LOCALE_CALL_RE = re.compile(r"\blocales\.([A-Z][A-Za-z0-9_]*)\s*\(")
ALLOWED_LOCALES_PACKAGE_CALLS = frozenset({"In"})


@dataclass
class LocaleCatalog:
    locale: str
    path: Path
    declared_locale: str | None = None
    messages: dict[str, str] = field(default_factory=dict)
    metadata: dict[str, Any] = field(default_factory=dict)
    placeholders: dict[str, set[str]] = field(default_factory=dict)
    duplicates: list[str] = field(default_factory=list)
    non_string_messages: list[str] = field(default_factory=list)


@dataclass
class AuditResult:
    source_locale: str
    catalogs: dict[str, LocaleCatalog]
    parse_errors: list[str]
    locale_tag_mismatches: list[str]
    missing_source_metadata: list[str]
    orphan_metadata: dict[str, list[str]]
    missing_by_locale: dict[str, list[str]]
    extra_by_locale: dict[str, list[str]]
    placeholder_mismatches: dict[str, dict[str, list[str]]]
    icu_syntax_errors: list[str]
    gen_missing_keys: list[str]
    gen_stale_keys: list[str]
    gen_missing_file: bool
    direct_locale_calls: list[str]
    unused_keys: list[str]
    english_copies: dict[str, list[str]]

    def has_errors(
        self,
        *,
        fail_on_english_copy: bool = False,
        fail_on_unused: bool = False,
    ) -> bool:
        return bool(
            self.parse_errors
            or self.locale_tag_mismatches
            or any(c.duplicates for c in self.catalogs.values())
            or any(c.non_string_messages for c in self.catalogs.values())
            or self.missing_source_metadata
            or self.orphan_metadata
            or self.missing_by_locale
            or self.extra_by_locale
            or self.placeholder_mismatches
            or self.icu_syntax_errors
            or self.gen_missing_file
            or self.gen_missing_keys
            or self.gen_stale_keys
            or self.direct_locale_calls
            or (fail_on_english_copy and self.english_copies)
            or (fail_on_unused and self.unused_keys)
        )


def parse_args(argv: list[str] | None = None) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description=(
            "Audit KyanDB ICU ARB catalogs for missing/extra keys, duplicate JSON keys, "
            "source @key descriptions, ICU placeholder drift, catalog_gen.go sync, "
            "and direct locales.<Key>() calls in internal/ui."
        )
    )
    parser.add_argument(
        "--config",
        type=Path,
        default=DEFAULT_CONFIG,
        help="Path to mygo.json (used to locate i18n.dir and i18n.source).",
    )
    parser.add_argument(
        "--locale-dir",
        type=Path,
        default=None,
        help="Override locale directory (default: from mygo.json i18n.dir).",
    )
    parser.add_argument(
        "--source-locale",
        default=None,
        help="Override source locale tag (default: from mygo.json i18n.source).",
    )
    parser.add_argument(
        "--ui-root",
        type=Path,
        default=DEFAULT_UI_ROOT,
        help="UI Go source directory scanned for accessor usage.",
    )
    parser.add_argument(
        "--fail-on-english-copy",
        action="store_true",
        help="Treat non-source values identical to the source message as errors.",
    )
    parser.add_argument(
        "--fail-on-unused",
        action="store_true",
        help="Treat catalog keys not referenced under internal/ui as errors.",
    )
    parser.add_argument(
        "--show-english-copies",
        action="store_true",
        help="Print individual English-copy keys instead of only the count.",
    )
    parser.add_argument(
        "--show-unused",
        action="store_true",
        help="Print individual unused keys instead of only the count.",
    )
    parser.add_argument(
        "--show-all",
        action="store_true",
        help="Print every finding without truncating long lists.",
    )
    parser.add_argument(
        "--run-mygo-check",
        action="store_true",
        help="Also run `mygo i18n check` for full CLDR plural and type validation.",
    )
    return parser.parse_args(argv)


def resolve_i18n_config(
    config_path: Path, locale_dir_override: Path | None, source_override: str | None
) -> tuple[Path, str]:
    locale_dir = config_path.parent / "locales"
    source_locale = "en"
    if config_path.exists():
        raw = json.loads(config_path.read_text(encoding="utf-8"))
        i18n_cfg = raw.get("i18n") or {}
        if isinstance(i18n_cfg, dict):
            if i18n_cfg.get("dir"):
                locale_dir = config_path.parent / str(i18n_cfg["dir"])
            if i18n_cfg.get("source"):
                source_locale = str(i18n_cfg["source"])
    if locale_dir_override is not None:
        locale_dir = locale_dir_override
    if source_override is not None:
        source_locale = source_override
    return locale_dir, source_locale


def _skip_ws(text: str, pos: int) -> int:
    while pos < len(text) and text[pos].isspace():
        pos += 1
    return pos


def extract_icu_placeholders(text: str) -> set[str]:
    """Parse ICU MessageFormat and return all referenced argument names.

    Raises ValueError on unbalanced braces or malformed argument heads.
    """
    names: set[str] = set()
    end = _parse_icu_segment(text, 0, stop_at_brace=False, names=names)
    if end != len(text):
        raise ValueError(f"unexpected '}}' at offset {end}")
    return names


def _parse_icu_segment(
    text: str, pos: int, *, stop_at_brace: bool, names: set[str]
) -> int:
    n = len(text)
    in_quote = False
    while pos < n:
        ch = text[pos]
        if in_quote:
            if ch == "'":
                if pos + 1 < n and text[pos + 1] == "'":
                    pos += 2
                    continue
                in_quote = False
            pos += 1
            continue
        if ch == "'":
            if pos + 1 < n and text[pos + 1] == "'":
                pos += 2
                continue
            # ICU apostrophe only starts quoting before syntax characters.
            if pos + 1 < n and text[pos + 1] in "{}#":
                in_quote = True
                pos += 1
                continue
            pos += 1
            continue
        if ch == "}":
            if stop_at_brace:
                return pos
            raise ValueError(f"unmatched '}}' at offset {pos}")
        if ch == "{":
            pos = _parse_icu_arg(text, pos + 1, names)
            continue
        pos += 1
    if stop_at_brace:
        raise ValueError("unclosed '{' in message")
    return pos


def _parse_icu_arg(text: str, pos: int, names: set[str]) -> int:
    n = len(text)
    pos = _skip_ws(text, pos)
    start = pos
    while pos < n and text[pos] not in ",}" and not text[pos].isspace():
        pos += 1
    name = text[start:pos]
    if not name or not re.fullmatch(r"[A-Za-z_][A-Za-z0-9_]*", name):
        raise ValueError(f"invalid ICU argument name {name!r}")
    names.add(name)

    pos = _skip_ws(text, pos)
    if pos >= n:
        raise ValueError(f"unclosed '{{' for argument {name!r}")
    if text[pos] == "}":
        return pos + 1
    if text[pos] != ",":
        raise ValueError(f"expected ',' or '}}' after argument {name!r}")

    pos = _skip_ws(text, pos + 1)
    type_start = pos
    while pos < n and text[pos] not in ",}" and not text[pos].isspace():
        pos += 1
    arg_type = text[type_start:pos]
    pos = _skip_ws(text, pos)
    if pos >= n:
        raise ValueError(f"unclosed '{{' in {name!r}")
    if text[pos] == "}":
        return pos + 1
    if text[pos] != ",":
        raise ValueError(f"expected ',' or '}}' after type {arg_type!r}")
    pos += 1

    if arg_type in {"plural", "select", "selectordinal"}:
        return _parse_icu_choices(text, pos, names)

    # Simple style (number/date/time): advance to the closing brace.
    while pos < n and text[pos] != "}":
        if text[pos] == "{":
            raise ValueError(f"unexpected '{{' inside {arg_type!r} style")
        pos += 1
    if pos >= n:
        raise ValueError(f"unclosed '{{' in {arg_type!r} argument {name!r}")
    return pos + 1


def _parse_icu_choices(text: str, pos: int, names: set[str]) -> int:
    n = len(text)
    saw_other = False
    while True:
        pos = _skip_ws(text, pos)
        if pos >= n:
            raise ValueError("unclosed plural/select block")
        if text[pos] == "}":
            if not saw_other:
                raise ValueError("plural/select block is missing required 'other' branch")
            return pos + 1

        sel_start = pos
        while pos < n and not text[pos].isspace() and text[pos] not in "{}":
            pos += 1
        selector = text[sel_start:pos]
        if not selector:
            raise ValueError(f"expected branch selector at offset {pos}")
        if selector.startswith("offset:"):
            continue
        if selector == "other":
            saw_other = True

        pos = _skip_ws(text, pos)
        if pos >= n or text[pos] != "{":
            raise ValueError(f"expected '{{' after selector {selector!r}")
        pos = _parse_icu_segment(text, pos + 1, stop_at_brace=True, names=names)
        if pos >= n or text[pos] != "}":
            raise ValueError(f"unclosed branch for selector {selector!r}")
        pos += 1


def load_catalogs(locale_dir: Path) -> tuple[dict[str, LocaleCatalog], list[str], list[str]]:
    catalogs: dict[str, LocaleCatalog] = {}
    parse_errors: list[str] = []
    icu_errors: list[str] = []

    if not locale_dir.is_dir():
        return catalogs, [f"locale directory does not exist: {locale_dir}"], icu_errors

    for json_file in sorted(locale_dir.glob("*.json")):
        locale = json_file.stem
        catalog = LocaleCatalog(locale=locale, path=json_file)
        duplicates: list[str] = []

        def object_pairs_hook(pairs: list[tuple[str, Any]]) -> dict[str, Any]:
            out: dict[str, Any] = {}
            for k, v in pairs:
                if k in out:
                    duplicates.append(k)
                out[k] = v
            return out

        try:
            raw = json.loads(
                json_file.read_text(encoding="utf-8"),
                object_pairs_hook=object_pairs_hook,
            )
        except Exception as error:  # noqa: BLE001 - report exact JSON failure.
            parse_errors.append(f"{json_file}: {error}")
            continue

        if not isinstance(raw, dict):
            parse_errors.append(f"{json_file}: top-level JSON value must be an object")
            continue

        catalog.duplicates = sorted(set(duplicates))
        for key, value in raw.items():
            if key == "@@locale":
                catalog.declared_locale = str(value)
                continue
            if key.startswith("@"):
                catalog.metadata[key[1:]] = value
                continue
            if not isinstance(value, str):
                catalog.non_string_messages.append(key)
                continue
            catalog.messages[key] = value
            try:
                catalog.placeholders[key] = extract_icu_placeholders(value)
            except ValueError as err:
                icu_errors.append(f"{json_file.name}:{key}: {err}")
                catalog.placeholders[key] = set()

        catalogs[locale] = catalog

    return catalogs, parse_errors, icu_errors


def scan_generated_catalog(locale_dir: Path) -> tuple[bool, dict[str, str]]:
    """Return (exists, {key: MethodName}) parsed from locales/catalog_gen.go."""
    gen_file = locale_dir / "catalog_gen.go"
    if not gen_file.exists():
        return False, {}
    text = gen_file.read_text(encoding="utf-8")
    key_to_method: dict[str, str] = {}
    for match in GENERATED_METHOD_RE.finditer(text):
        method_name, key = match.group(1), match.group(2)
        key_to_method[key] = method_name
    return True, key_to_method


def strip_go_comments(source: str) -> str:
    """Remove // and /* */ comments while keeping string literals intact."""
    out: list[str] = []
    i, n = 0, len(source)
    while i < n:
        ch = source[i]
        if ch == '"':
            start = i
            i += 1
            while i < n and source[i] != '"':
                if source[i] == "\\" and i + 1 < n:
                    i += 2
                else:
                    i += 1
            i = min(i + 1, n)
            out.append(source[start:i])
        elif ch == "`":
            end = source.find("`", i + 1)
            if end == -1:
                out.append(source[i:])
                break
            out.append(source[i : end + 1])
            i = end + 1
        elif ch == "/" and i + 1 < n and source[i + 1] == "/":
            end = source.find("\n", i)
            if end == -1:
                break
            out.append("\n")
            i = end + 1
        elif ch == "/" and i + 1 < n and source[i + 1] == "*":
            end = source.find("*/", i + 2)
            if end == -1:
                break
            # Preserve line breaks so reported line numbers stay accurate.
            out.append("\n" * source[i : end + 2].count("\n"))
            i = end + 2
        else:
            out.append(ch)
            i += 1
    return "".join(out)


def scan_ui_usage(
    ui_root: Path, key_to_method: dict[str, str]
) -> tuple[list[str], set[str]]:
    """Return (direct_global_calls, used_method_names) from non-test Go UI files."""
    direct_calls: list[str] = []
    used_methods: set[str] = set()
    method_set = set(key_to_method.values())

    if not ui_root.exists():
        return direct_calls, used_methods

    method_call_re = re.compile(r"\.([A-Z][A-Za-z0-9_]*)\s*\(")

    for go_file in sorted(ui_root.rglob("*.go")):
        if go_file.name.endswith("_test.go"):
            continue
        raw = go_file.read_text(encoding="utf-8")
        cleaned = strip_go_comments(raw)
        rel = go_file.relative_to(ui_root.parent.parent) if ui_root.parent.parent in go_file.parents else go_file

        for line_no, line in enumerate(cleaned.splitlines(), start=1):
            for m in DIRECT_LOCALE_CALL_RE.finditer(line):
                name = m.group(1)
                if name not in ALLOWED_LOCALES_PACKAGE_CALLS:
                    direct_calls.append(f"{rel}:{line_no}: locales.{name}()")
            for m in method_call_re.finditer(line):
                name = m.group(1)
                if name in method_set:
                    used_methods.add(name)

    return direct_calls, used_methods


def audit(
    locale_dir: Path, source_locale: str, ui_root: Path
) -> AuditResult:
    catalogs, parse_errors, icu_errors = load_catalogs(locale_dir)

    locale_tag_mismatches: list[str] = []
    for locale, cat in catalogs.items():
        if cat.declared_locale is not None and cat.declared_locale != locale:
            locale_tag_mismatches.append(
                f"{cat.path.name}: @@locale is {cat.declared_locale!r}, expected {locale!r}"
            )

    source_cat = catalogs.get(source_locale)
    if source_cat is None:
        parse_errors.append(
            f"source catalog {source_locale}.json not found in {locale_dir}"
        )

    source_keys = sorted(source_cat.messages) if source_cat else []
    source_key_set = set(source_keys)

    # Every message in the source catalog must carry a non-empty @key description.
    missing_source_metadata: list[str] = []
    if source_cat is not None:
        for key in source_keys:
            meta = source_cat.metadata.get(key)
            if not isinstance(meta, dict) or not str(meta.get("description", "")).strip():
                missing_source_metadata.append(key)

    orphan_metadata: dict[str, list[str]] = {}
    for locale, cat in catalogs.items():
        orphans = sorted(k for k in cat.metadata if k not in cat.messages)
        if orphans:
            orphan_metadata[locale] = orphans

    missing_by_locale: dict[str, list[str]] = {}
    extra_by_locale: dict[str, list[str]] = {}
    for locale, cat in catalogs.items():
        if locale == source_locale:
            continue
        missing = [k for k in source_keys if k not in cat.messages]
        if missing:
            missing_by_locale[locale] = missing
        extra = sorted(k for k in cat.messages if k not in source_key_set)
        if extra:
            extra_by_locale[locale] = extra

    placeholder_mismatches: dict[str, dict[str, list[str]]] = {}
    if source_cat is not None:
        for key in source_keys:
            expected = source_cat.placeholders.get(key, set())
            diffs: dict[str, list[str]] = {}
            for locale, cat in catalogs.items():
                if locale == source_locale or key not in cat.messages:
                    continue
                actual = cat.placeholders.get(key, set())
                if actual != expected:
                    diffs[locale] = sorted(actual)
            if diffs:
                diffs[source_locale] = sorted(expected)
                placeholder_mismatches[key] = diffs

    gen_exists, key_to_method = scan_generated_catalog(locale_dir)
    gen_key_set = set(key_to_method)
    gen_missing_keys = sorted(source_key_set - gen_key_set) if gen_exists else []
    gen_stale_keys = sorted(gen_key_set - source_key_set) if gen_exists else []

    direct_locale_calls, used_methods = scan_ui_usage(ui_root, key_to_method)
    unused_keys = (
        sorted(k for k, method in key_to_method.items() if method not in used_methods)
        if gen_exists
        else []
    )

    english_copies: dict[str, list[str]] = {}
    if source_cat is not None:
        for locale, cat in catalogs.items():
            if locale == source_locale:
                continue
            copied = []
            for key, src_text in source_cat.messages.items():
                local_text = cat.messages.get(key)
                if not local_text or local_text != src_text:
                    continue
                if not src_text.strip() or not re.search(r"[A-Za-z]{4,}", src_text):
                    continue
                copied.append(key)
            if copied:
                english_copies[locale] = copied

    return AuditResult(
        source_locale=source_locale,
        catalogs=catalogs,
        parse_errors=parse_errors,
        locale_tag_mismatches=locale_tag_mismatches,
        missing_source_metadata=missing_source_metadata,
        orphan_metadata=orphan_metadata,
        missing_by_locale=missing_by_locale,
        extra_by_locale=extra_by_locale,
        placeholder_mismatches=placeholder_mismatches,
        icu_syntax_errors=icu_errors,
        gen_missing_keys=gen_missing_keys,
        gen_stale_keys=gen_stale_keys,
        gen_missing_file=not gen_exists,
        direct_locale_calls=direct_locale_calls,
        unused_keys=unused_keys,
        english_copies=english_copies,
    )


def print_limited(title: str, items: list[str], limit: int | None = 30) -> None:
    print(f"\n{title}: {len(items)}")
    visible = items if limit is None else items[:limit]
    for item in visible:
        print(f"  - {item}")
    if limit is not None and len(items) > limit:
        print(f"  ... {len(items) - limit} more")


def print_result(result: AuditResult, args: argparse.Namespace) -> None:
    limit = None if args.show_all else 30
    source_count = (
        len(result.catalogs[result.source_locale].messages)
        if result.source_locale in result.catalogs
        else 0
    )
    print(
        f"Locales ({len(result.catalogs)}, source={result.source_locale}): "
        f"{', '.join(sorted(result.catalogs))}"
    )
    print(f"Source messages: {source_count}")

    if result.parse_errors:
        print_limited("Parse errors", result.parse_errors, limit)
    if result.locale_tag_mismatches:
        print_limited("@@locale mismatches", result.locale_tag_mismatches, limit)
    for locale, cat in sorted(result.catalogs.items()):
        if cat.duplicates:
            print_limited(f"Duplicate keys in {locale}.json", cat.duplicates, limit)
        if cat.non_string_messages:
            print_limited(
                f"Non-string message values in {locale}.json",
                cat.non_string_messages,
                limit,
            )
    if result.icu_syntax_errors:
        print_limited("ICU MessageFormat syntax errors", result.icu_syntax_errors, limit)
    if result.missing_source_metadata:
        print_limited(
            f"Missing @key description in {result.source_locale}.json",
            result.missing_source_metadata,
            limit,
        )
    for locale, keys in sorted(result.orphan_metadata.items()):
        print_limited(f"Orphan @metadata keys in {locale}.json", keys, limit)
    for locale, keys in sorted(result.missing_by_locale.items()):
        print_limited(f"Missing keys in {locale}.json", keys, limit)
    for locale, keys in sorted(result.extra_by_locale.items()):
        print_limited(f"Extra keys in {locale}.json (not in {result.source_locale}.json)", keys, limit)
    if result.placeholder_mismatches:
        lines = [
            f"{key}: " + ", ".join(f"{loc}={vals}" for loc, vals in sorted(by_loc.items()))
            for key, by_loc in sorted(result.placeholder_mismatches.items())
        ]
        print_limited("ICU placeholder mismatches", lines, limit)
    if result.gen_missing_file:
        print("\nGenerated catalog missing: run `mygo generate` to write locales/catalog_gen.go")
    if result.gen_missing_keys:
        print_limited(
            "Keys missing from catalog_gen.go (run `mygo generate`)",
            result.gen_missing_keys,
            limit,
        )
    if result.gen_stale_keys:
        print_limited(
            "Stale keys in catalog_gen.go (run `mygo generate`)",
            result.gen_stale_keys,
            limit,
        )
    if result.direct_locale_calls:
        print_limited(
            "Direct locales.<Key>() calls in UI (use msgs(c) / locales.In(c.Locale()))",
            result.direct_locale_calls,
            limit,
        )

    if result.unused_keys:
        if args.show_unused or args.show_all or args.fail_on_unused:
            print_limited("Unused catalog keys", result.unused_keys, limit)
        else:
            print(f"\nUnused catalog keys (warning): {len(result.unused_keys)} (pass --show-unused to list)")

    if result.english_copies:
        total_copies = sum(len(keys) for keys in result.english_copies.values())
        if args.show_english_copies or args.show_all or args.fail_on_english_copy:
            for locale, keys in sorted(result.english_copies.items()):
                print_limited(f"Untranslated copies in {locale}.json", keys, limit)
        else:
            print(
                f"\nPossible untranslated copies (warning): {total_copies} "
                "(pass --show-english-copies to list)"
            )


def run_mygo_i18n_check(project_dir: Path) -> int:
    env = os.environ.copy()
    env.setdefault("CGO_ENABLED", "0")
    cmd = ["go", "run", "github.com/egoist/mygo/cmd/mygo", "i18n", "check", str(project_dir)]
    print("\n+", " ".join(cmd), flush=True)
    proc = subprocess.run(cmd, cwd=project_dir, env=env)
    return proc.returncode


def main(argv: list[str] | None = None) -> int:
    args = parse_args(argv)
    locale_dir, source_locale = resolve_i18n_config(
        args.config, args.locale_dir, args.source_locale
    )
    result = audit(locale_dir, source_locale, args.ui_root)
    print_result(result, args)

    failed = result.has_errors(
        fail_on_english_copy=args.fail_on_english_copy,
        fail_on_unused=args.fail_on_unused,
    )
    if args.run_mygo_check:
        rc = run_mygo_i18n_check(args.config.parent)
        if rc != 0:
            failed = True

    if failed:
        print("\ni18n audit FAILED")
        return 1
    print("\ni18n audit OK")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
