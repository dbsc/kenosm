# Design assets library

Everything in this directory is available to you inside the workspace. Use it to
make the sites and UIs you build genuinely polished instead of plain.

## What's here

- `icons/lucide/<name>.svg` — ~2000 Lucide icons (MIT). Stroke icons, 24×24.
  e.g. `icons/lucide/arrow-right.svg`, `icons/lucide/zap.svg`, `icons/lucide/menu.svg`.
- `icons/tabler/<name>.svg` — ~5500 Tabler icons (MIT). Another style of stroke
  icons if Lucide doesn't have what you need.
- `spinners/<name>.svg` — self-contained animated SVG loaders (SMIL), e.g.
  `spinners/ring-resize.svg`, `spinners/wind-toy.svg`.
- `templates/` — three reference pages showing the design quality expected.
  Read one, then reuse its techniques (never copy it verbatim into a real site).
- `DESIGN.md` — the full design system: named aesthetics, anti-patterns to
  avoid (AI slop), OKLCH design tokens, typography and motion rules. Read this
  BEFORE building any UI.
- `uiux/` — a searchable UI/UX knowledge base (BM25 over CSVs). Query it to get
  concrete, product-specific style/palette/font/UX guidance. See below.

## How to use an icon

Read the SVG with `read_file`, then inline it directly into your HTML:

```html
<svg xmlns="http://www.w3.org/2000/svg" width="20" height="20" viewBox="0 0 24 24"
     fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"
     stroke-linejoin="round">
  <path d="M5 12h14"/><path d="m12 5 7 7-7 7"/>
</svg>
```

`stroke="currentColor"` means the icon takes the text colour, so it inherits
button/link colours automatically.

## Design rules — make it look expensive

1. **Never ship unstyled HTML.** Every page you build needs a real design system:
   a max-width container, a colour palette (3–5 tokens on `:root`), consistent
   spacing, and a font from Google Fonts (e.g. Inter, Space Grotesk).
2. **Colour**: use gradients (`linear-gradient`, `radial-gradient`) and a mesh or
   aurora background rather than flat grey. Dark mode via
   `@media (prefers-color-scheme: dark)`.
3. **Depth / "3D"**: layered shadows, blur (glassmorphism `backdrop-filter`), and
   CSS 3D transforms (`perspective`, `rotateX/rotateY`, `translateZ`) for card
   tilt on hover. For real 3D (spinning geometry, particles) use Three.js from
   cdnjs: `https://cdnjs.cloudflare.com/ajax/libs/three.js/r128/three.min.js`.
4. **Motion**: subtle transitions (150–300ms), a hover lift, and one tasteful
   animation (fade/slide on scroll, animated gradient, or an SVG spinner) — not
   everything at once.
5. **Icons everywhere**: a button or list item looks unfinished without an icon.
   Pull from `icons/lucide/` and inline it.

## Templates (read for reference)

- `templates/3d-hero.html` — Three.js animated hero (rotating torus + particles),
  glassmorphism nav, gradient text. The "wow" landing opener.
- `templates/glass-dashboard.html` — glass cards, CSS 3D tilt on hover, metric
  tiles with Lucide icons, dark theme.
- `templates/gradient-landing.html` — aurora gradient mesh background, scroll
  reveal, pricing cards with 3D hover tilt.

After building a page, always open it with `browser_navigate` and screenshot it
to confirm it actually looks good before finishing.

## Searchable UI/UX knowledge base (`uiux/`)

Run the search script with `run_command` to pull concrete guidance before
picking an aesthetic or palette. It uses pure stdlib Python (no deps).

```bash
python3 assets/uiux/scripts/search.py "<query>" --domain style
```

`--domain` is one of: `style`, `color`, `typography`, `product`, `ux`,
`landing`, `chart`, `icons`, `google-fonts`, `react`, `web`, `gsap`.
Default is `style`. Add `-n 3` to get more rows, or `--stack <name>` to scope
to a tech stack (default `html-tailwind`; see `assets/uiux/data/stacks/` for
the full list).

To generate a whole coherent design system in one shot:

```bash
python3 assets/uiux/scripts/search.py "<product>" --design-system \
  --variance 7 --motion 5 --density 6
```

That emits a self-consistent style + palette + typography + motion recipe.
Use its output to lock the named aesthetic and OKLCH tokens before writing any
HTML.

