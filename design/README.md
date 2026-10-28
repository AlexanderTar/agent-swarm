# Swarm design system

Dark only. One brand violet (`--violet-500 #6e5ce6`, the board's), Onest for
text and Fragment Mono for code. `design/tokens.json` is the single source;
`design/tokens.css` is generated from it, never edited by hand.

```sh
node design/scripts/gen-tokens-css.mjs         # regenerate
node design/scripts/gen-tokens-css.mjs --check # fail when stale (CI)
```

`node scripts/ds-artifact-check.mjs <downloaded-project-dir>` diffs `design/`
against the published artifact copy. Download the artifact's `project/` folder
first (Claude's artifact download), then fetch each upload: read
`design/design-system.json` `assetGroups.<Group>.files[<file>].blob` for the
blob id, download the blob, and save it as `project/assets/<Group>/<file>`
before running the check (the file listing only carries
`project/assets/<Group>/README.md`; uploads arrive by blob id).
`design/design-system.json` is compared with the page-owned keys (`sections`,
`blobs`, `docs`, `lastChange`) masked out. Repo-only files (`design/scripts/`,
`design/tokens.css`) are skipped; artifact-only extras (`archived/`,
`uploads/`) are ignored. `design/tokens.schema.json` ships with the artifact
(the republish includes it), so the check expects it in `project/`.

SVGs compare after canonical serialization (empty-element tags, comments and
inter-tag whitespace are normalized): the upload store rewrites SVG markup on
publish while the vector stays the same. The `size` values in
`design-system.json` describe the uploaded bytes.

Repo-only token extras: the artifact page drops or clamps `gradient-hero`
(over the page gradient length cap), the `{alias}` shadow forms and the
`drop-shadow()` icon shadow, so the repo's `tokens.json` keeps them and the
generated `tokens.css` keeps the matching `var(--…)` variables. A test lists
every token the page would drop against this named set. Aliases always use the
`{name}` form in `tokens.json`; raw `var(--…)` never appears there (a test
enforces this). Multi-layer values keep real comma separators with one layer
per line; the generator wraps them, and a bare newline without a comma is
invalid as data (a test enforces this). Token `usage` strings are documented
in `tokens.json`; the generated CSS omits them.

## Brand

Swarm runs your coding agents as a team. The voice is plain and developer to
developer: short sentences, no internals (no tmux, worktrees, ports, daemon),
no platform names. One violet does the brand work; everything else is night
and neutral surfaces.

- App icon: `assets/Logos/app-icon.svg` (1024). The gradient
  `#5B7CFA → #7B5CF0 (55%) → #9B4FE0` is art, never behind text.
- Glyph: `assets/Logos/glyph.svg` (24). Single colour, `currentColor`.
- Logo use: monochrome marks at one size, always next to the product name,
  unaltered in shape. Agent logos on the site follow the same rule, with the
  trademark line on the page. Never recolour, stretch or rearrange the marks.

## Colour

Primitives: brand violet, night surfaces, the neutral ladder (plus
`--neutral-300` for long-form body copy), and four status colours. Semantic
names keep the board's values (`--background`, `--card`, `--primary`, …) so
the board can adopt this file unchanged; `--primary-hover`,
`--primary-active` and `--foreground-soft` are new. White on violet is
4.84:1 (hover 4.53:1, pressed 5.84:1); body copy on night is 9.59:1.

## Type

- Sans: Onest Variable (`design/fonts/onest-latin.woff2`, OFL in `OFL-Onest.txt`).
- Mono: Fragment Mono 400 (`design/fonts/fragment-mono-latin-400.woff2`, OFL in
  `OFL-Fragment-Mono.txt`) for code, paths, keys and eyebrows.
- Display 32→56 / title 28→40 / wordmark 28→36 (`--weight-display 650`);
  heading 20 600; lead 18→20; body 16; small 14; caption 12. Body line-height
  1.6, code 1.7. Text scales in rem and survives 200% zoom.

## Space, radius, elevation

4px base (`--space-1` … `--space-28`), 1152 content width, section rhythm
64→112. Radius 4 / 6 (buttons) / 8 (cards, code) / 12 (screenshot frame) /
full (pills, chips). One screenshot frame shadow, one icon drop shadow, one
focus ring. Two brand gradients plus the hero glow; motion is one 120/200ms
ease-out. No animation on scroll; `prefers-reduced-motion` disables the rest.

## Components

Each `components/<Name>/preview.html` starts with an `@dsCard` marker and
renders the component against the generated tokens: Colors, Type, Logo,
Buttons, StatusPills, Cover. The Logo preview inlines the app icon SVG so the
same file works in the repo and on the artifact page with no path reference.
