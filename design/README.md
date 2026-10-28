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
first (Claude's artifact download), then point the check at it: asset uploads
land at `project/assets/<Group>/<file>`, compared byte for byte against
`design/assets/<Group>/<file>`. `design/design-system.json` is compared with
the page-owned keys (`sections`, `blobs`, `docs`, `lastChange`) masked out.
Repo-only files (`scripts/`, `tokens.css`) are skipped; artifact-only extras
(`archived/`, `uploads/`) are ignored.

Repo-only token extras: the artifact page drops or clamps `gradient-hero`
(over the page gradient length cap), the `{alias}` shadow forms (the page
resolves them to literals) and the `drop-shadow()` icon shadow, so the repo's
`tokens.json` keeps them and the generated `tokens.css` keeps the matching
`var(--…)` variables. Aliases always use the `{name}` form in `tokens.json`;
raw `var(--…)` never appears there (a test enforces this).

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
