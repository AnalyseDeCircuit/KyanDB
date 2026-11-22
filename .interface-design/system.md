# KyanDB Interface System

The source of truth for how KyanDB looks. Tokens live in `internal/ui/theme`; this file records the decisions and measurements behind them. Read it before adding or changing any view.

## Direction

- **Who**: a developer or operator inspecting or correcting live data, often on production.
- **Task**: browse schemas, scan wide tables, edit safely, review and commit transactions.
- **Feel**: native macOS desktop instrument—calm neutral planes, borderless inline bars, quiet hairlines, and soft surface washes instead of nested boxes or neon accents. Sibling to OxideTerm's restraint, adapted for wide database grids rather than dual SSH tool rails.
- **Mode**: Operate. Zero boxed omnibars, zero multi-card option grids, zero decorative filler copy.

## Color

One calm mineral kyanite hue, neutral obsidian/paper planes, three restrained signals, and soft cell washes for staged edits. Every color in a view comes from `theme.Palette`; never write a hex value in view packages. All text/surface pairs pass WCAG AA (`≥ 4.5:1`, enforced by `theme_test.go`).

| Token | Light | Dark | Use |
| --- | --- | --- | --- |
| `Canvas` | `#ffffff` | `#181a1d` | Data surfaces: grid, editor, active tab |
| `Matrix` | `#f6f8fa` | `#1d2024` | Chrome: titlebar, sidebar, tab strip, bars, grid header |
| `Inset` | `#eef1f4` | `#131518` | Recessed modal inputs and segmented tracks |
| `Seam` | `#0f1720` 8% | `#ffffff` 7% | Low-alpha 1 DIP hairlines between regions |
| `SeamStrong` | `#0f1720` 14% | `#ffffff` 13% | Subtle overlay and button borders |
| `Ink` | `#1b2025` | `#e4e7eb` | Values, primary text |
| `InkSoft` | `#434c56` | `#aeb6bf` | Labels, headers, ghost buttons |
| `InkFaint` | `#5c6670` | `#8c95a0` | Metadata, routes, inactive tabs, icons |
| `InkGhost` | `#65707b` | `#828c97` | Italic `NULL`, row numbers, counts, chevrons |
| `Kyanite` | `#0a7a88` | `#0b7c8a` | Primary CTA fill (white text `≥ 4.9:1`), active tab top bar |
| `KyaniteInk` | `#07606b` | `#58c4d2` | Cursor cell outline/text on `KyaniteCell` |
| `KyaniteWash` | Kyanite 7% | Kyanite 11% | Selected grid rows |
| `KyaniteCell` | Kyanite 16% | Kyanite 24% | Cursor cell inside the selection |
| `Oxide` / `OxideFill` | `#bd3238` / `#bd3238` | `#e56b70` / `#b8373d` | `prod` signal, delete gutter, destructive CTA (`white ≥ 5.7:1`) |
| `Amber` / `AmberInk` | `#b88014` / `#8a5e08` | `#d4a03c` / `#d9a94e` | `staging`, dirty tab dot, modified gutter/tally |
| `Jade` | `#1b784b` | `#4eb882` | `dev`, inserted gutter/tally |
| `ModifiedBg` / `InsertedBg` / `DeletedBg` | soft 9–13% tints | soft 12–14% tints | Calm cell surface washes for staged edits (replaces dotted underlines) |

## Corner Geometry (Superellipse + Adjustable Scale)

- **Continuous superellipse curves**: `mygo`'s GPU shaders (Metal, D3D11, OpenGL) and software rasterizer draw every `Element.Radius(...)` with Core Animation's continuous superellipse curve (`QuartzCore` `supercircle_sdf`).
- **User-adjustable base radius**: never hardcode pixel radius literals in views (except 50% circular dots). Always read `k.R` (`theme.RadiiFor(base)` for `base` in `0..16` DIP, default `6`): `XS = max(r-4, 0)`, `SM = max(r-2, 0)`, `MD = r`, `LG = r+4` (`0` across the board when `r == 0`).

## Layout & Dual Horizontal Datums

Two unbroken horizontal hairlines align the sidebar and workspace across the split:

| Element | Size | Notes |
| --- | --- | --- |
| Native Title Bar | `TitleBarHeight` 28 | Traffic lights left; quiet plain-text connection context centered (no pill/border); sidebar/refresh icons right; 2 DIP top edge strip on `staging`/`prod` |
| Datum #1 | `HeaderHeight` 34 | Left: clickable `● connection ⌄` switcher + `+` button; Right: document tab strip (`2` DIP Kyanite top bar on active tab) |
| Datum #2 | `SubBarHeight` 30 | Left: borderless inline `Filter` input; Right: borderless inline `WHERE` bar (table) or `Run / Explain` bar (query) |
| Grid Row / Header | `RowHeight` 24 / 26 | Virtualized `ui.Table` with `CellSelect`, row-number gutter (`48`), PK icon, trailing flexible spacer column |
| Staged Ledger | 34 | Appears only with pending edits; ghost `Discard` + `Review SQL`, primary/danger `Commit` (locked to read-only text when `ReadOnly`) |
| Status Bar | 24 | Left: connection dot + name + read-only; Right: tabular row count + elapsed time |

## Modals & Native Forms

- **Floating elevation (`Z > 0`)**: `ui.DialogBase` with `k.R.LG`, `1` DIP `SeamStrong`, soft drop shadow; in-plane surfaces (`Z = 0`) carry zero shadows.
- **Two-column desktop form rows (`labelColWidth = 124`)**: right-aligned labels on the left, controls on the right—no stacked web cards, no three-card environment grids, no box-around-switch wrappers. Flat underline tabs (`General / SSH Tunnel / Safety`) separate sections cleanly.
- **Review SQL modal**: clean header (`Review SQL · ● orders-primary`), summary strip with `Copy SQL`, read-only wrapped SQL preview (`SoftWrap: true`), calm protected-env notice strip, footer actions.

## Avoid

- Giant centered browser-style omnibar pills in the toolbar.
- Box-inside-box borders around `WHERE`, sidebar search, `NULL` values, toggles, or header badges.
- Three equal-width description cards for enum pickers.
- Neon dotted underlines or rainbow text inside grid cells (use soft `ModifiedBg`/`InsertedBg`/`DeletedBg` washes + clean italic `NULL`).
- Hardcoded hex literals or pixel radius numbers in view code.

## Motion System (`internal/ui/theme/motion.go`)
Disciplined, fast (`<= 180 ms`), non-bouncy desktop motion built on `mygo/ui`'s GPU compositor (`AnimatePose`, `Transition`, `AnimateColor`, `Match`):

| Token | Curve & Duration | Usage |
| --- | --- | --- |
| `HoverColor` | `110ms` `CubicBezier(0.23, 1, 0.32, 1)` (Oklab, `NonMotion`) | Button & icon button hover tint |
| `ApplyPress` | `110ms` `EaseOut`, `Scale: 0.97` | GPU-composited tactile button press |
| `PopoverTransition` | `150ms` in / `100ms` out, `Y: -4`, `Scale: 0.97`, `TransformOrigin(0, 0)` | Sidebar connection switcher popover |
| `ModalBackdropTransition` / `ModalPanelTransition` | `160–180ms` in / `120ms` out, `Y: 6`, `Scale: 0.96`, centered | Connection, Review SQL, and Host Key modals |
| `BarRevealTransition` | `180ms` in / `130ms` out, `Collapse: true`, `Y: 4` | Staged-edit ledger bar & inline SQL error banner |
| `GlideMatch(key)` | `170ms` `CubicBezier(0.77, 0, 0.175, 1)` | Active tab `2px` Kyanite bar, modal section underline, segmented thumb |
| `ui.Spinner` | 12-spoke native AppKit spinner (12 DIP, Kyanite) | Active running query indicator |

- **ReduceMotion**: Automatically honored by `mygo/ui` (positional/scale/collapse snaps immediately; gentle color/fade cues remain).
- **Never animate**: Data grid scrolling/selection/cell edits, keyboard shortcuts (`⌘B`/`⌘K`/`⌘W`/`⌘Enter`), or schema tree filtering.
