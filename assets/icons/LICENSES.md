# Icon sources and licences

The menu bar uses these files as single-colour template images (spec §21.1).

| File | Source | Licence |
|---|---|---|
| `claude.svg` | Simple Icons, `claude` (https://simpleicons.org, https://github.com/simple-icons/simple-icons) | CC0 1.0 Universal |
| `cursor.svg` | Simple Icons, `cursor` | CC0 1.0 Universal |
| `codex.svg` | LobeHub Icons, `@lobehub/icons-static-svg` `codex.svg` (https://github.com/lobehub/lobe-icons) | MIT, Copyright (c) 2023 LobeHub |
| `agy.svg` | LobeHub Icons, `@lobehub/icons-static-svg` `antigravity.svg` | MIT, Copyright (c) 2023 LobeHub |
| `muse.svg` | LobeHub Icons, `@lobehub/icons-static-svg` `meta.svg` (https://github.com/lobehub/lobe-icons) | MIT, Copyright (c) 2023 LobeHub |
| `swarm.svg` | Drawn for Agent Swarm | MIT, same as this repository |

Product names and logos are trademarks of their owners. They identify which agent a value belongs to.

## MIT licence (LobeHub Icons)

Permission is hereby granted, free of charge, to any person obtaining a copy of this software and associated documentation files (the "Software"), to deal in the Software without restriction, including without limitation the rights to use, copy, modify, merge, publish, distribute, sublicense, and/or sell copies of the Software, and to permit persons to whom the Software is furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.

## How the files were made

Copied on 2026-09-17 from `~/.superpowers/specs/research/2026-09-17-agent-swarm-go-rewrite/icons/`
(`si-claude.svg`, `si-cursor.svg`, `lobe-codex.svg`, `lobe-antigravity.svg`), then normalised with:

```bash
perl apps/menubar/scripts/normalize-icons.pl assets/icons/*.svg
```

The script sets a 24 × 24 size and expands packed SVG arc flags, which macOS's SVG renderer misreads.
NSImage loads the SVG files directly (macOS 14+), so no PDF or PNG copies are kept. `scripts/bundle.sh`
copies them into `Swarm.app/Contents/Resources/`.
