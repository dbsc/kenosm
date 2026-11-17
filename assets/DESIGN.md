# Design system — how to make sites look premium, not AI-generated

Read this before building any UI. The single biggest mistake is jumping into HTML
without choosing an aesthetic first, which produces "AI slop": the same Inter
font, a purple/blue gradient, and rounded white cards every time.

## Rule 0 — pick a direction BEFORE writing code

Choose exactly ONE aesthetic from the list below and commit to it. State your
choice in one line, then make every color, font, spacing and shadow decision
serve that direction. Do not blend two aesthetics.

## Never do these (AI slop, instantly recognizable)

- Inter as the default font with no typographic pairing.
- A purple-to-blue linear gradient hero on white.
- Rounded white cards with a soft drop shadow as the only layout.
- Generic "SaaS" three-feature + pricing + CTA template.
- Center-aligned everything, no grid, no rhythm, no contrast.
- No dark mode, no hover/focus states, no real icons.

## The 11 aesthetics

Pick one by name. ("Think…" is the vibe, not the implementation.)

1. **Swiss Minimalism** — Helvetica/Inter Tight, strict grid, massive white space,
   near-black on near-white, one accent only.
2. **Neumorphism** — soft extruded shadows on a matching background, monochrome
   pastel, rounded but not generic.
3. **Glassmorphism** — frosted `backdrop-filter` panels over a colorful aurora,
   thin 1px light borders, layered depth.
4. **Brutalism** — raw, anti-design: system fonts, visible borders, no rounded
   corners, loud color, oversized type.
5. **Claymorphism** — 3D soft clay blobs, chunky rounded shapes, inner+outer
   shadows, pastel palette.
6. **Aurora Mesh Gradient** — flowing multi-color radial gradients, blurred,
   animated slowly, dark or light.
7. **Retro-Futurism / Cyberpunk** — neon on near-black, monospace accents, grid
   lines, scanlines, glowing edges.
8. **3D Hyperrealism** — real depth and perspective, Three.js geometry, shadows,
   parallax, physical feel.
9. **Vibrant Block Maximalist** — big color blocks, loud clashing hues, bold
   type, full-bleed sections.
10. **Dark OLED Luxury** — true `#000` background, gold/cream accents, thin
    serif type, generous spacing, restrained.
11. **Organic Biomorphic** — earth tones, natural rounded shapes, warm soft
    shadows, serif body text.

## Theme presets (concrete directives)

Copy the directives for your chosen direction:

- **Cyberpunk** — neon on dark, monospace type, glitch/scan-line effects.
- **Editorial** — serif headlines, magazine grid, muted palette, pull quotes.
- **SaaS Minimal** — one accent color, system UI font, lots of air, card-based.
- **Dark OLED Luxury** — true `#000` bg, gold/cream accents, thin serif type.
- **Brutalist** — system fonts, visible borders, no rounded corners, loud color.
- **Retro-Futuristic** — gradient meshes, chrome, geometric shapes, purple/teal.
- **Organic/Natural** — earth tones, rounded shapes, warm shadows, serif body.
- **Art Deco** — gold + black, geometric patterns, Playfair Display, symmetry.

## Design tokens — one number controls the whole palette

Define colors as CSS variables on `:root` (and again under dark mode) derived
from a single brand hue using OKLCH. Never hardcode hex values in components.

```css
:root {
  --brand-hue: 250;
  --color-primary: oklch(0.6 0.2 var(--brand-hue));
  --color-bg: oklch(0.995 0.005 var(--brand-hue));
  --color-fg: oklch(0.15 0.02 var(--brand-hue));
  --color-muted: oklch(0.94 0.01 var(--brand-hue));
  --color-border: oklch(0.88 0.015 var(--brand-hue));
  /* typography scale, 4px spacing grid, radius, shadow tokens too */
}
@media (prefers-color-scheme: dark) {
  :root:not([data-theme="light"]) {
    --color-bg: oklch(0.13 0.02 var(--brand-hue));
    --color-fg: oklch(0.96 0.01 var(--brand-hue));
  }
}
```

Changing `--brand-hue` re-themes the whole page. Use brand-tinted neutrals, not
pure `#fff`/`#000` (except OLED luxury).

## Typography

- Pair a display face with a body face (e.g. Space Grotesk + Inter, Playfair
  Display + Inter, Fraunces + Inter). Never a single default sans-serif.
- Load real fonts from Google Fonts.
- Establish a clear scale (display/h1/h2/body/small) and line-height ~1.5.

## Motion

- Subtle and purposeful: 150–300ms transitions, one hover lift, one scroll
  reveal. Not everything animated at once.
- Respect `prefers-reduced-motion` (disable heavy animation when set).
- Prefer transform/opacity (GPU-friendly) over top/left/width.

## Polish checklist (before finishing)

- Keyboard focus styles, hover/active states, `aria-` labels, semantic elements.
- Consistent 4px/8px spacing grid, aligned baseline.
- Real content (no lorem ipsum in a shipped page).
- Verify in the browser: `browser_navigate` then screenshot, and fix anything
  that looks off. Screenshots are the source of truth for "does it look good".
