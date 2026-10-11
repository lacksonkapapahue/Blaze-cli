---
name: web-design-blaze-classic
description: Design or build pages, apps or components in the classic Blaze look from blazeinchue.netlify.app - paper #fcfcfc, ink #111110, one blue accent #1f4bd8, square edges, Inter + Inter Tight, mono uppercase labels, hairline-ruled grids and particle-dot hovers. Use for anything Blaze-branded or when the user asks for "the Blaze style".
tags: [design, web, css, brand, blaze]
version: 1
---

# Web design: classic Blaze

Studied from https://blazeinchue.netlify.app/ (HTML, `styles.css`, `script.js`). Quiet,
editorial and engineered: a near-white page, near-black ink at several opacities, **one** blue
accent, **no rounded corners anywhere**, content laid out in hairline-ruled tables and grids
instead of floating cards, mono uppercase labels, and a few precise interactions (particle
dots on hover, a glitching eyebrow, a pixel-tile theme switch).

Starter files in this skill's folder: `assets/tokens.css` (tokens, light and dark themes,
every component below) and `assets/starter.html` (topbar, hero with a data table, metrics,
services grid, timeline, CTA, footer, theme toggle). Copy both and adapt.

## 1. Tokens

| Token | Light | Dark (`:root[data-theme="dark"]`) | Use |
|---|---|---|---|
| `--paper` | `#fcfcfc` | `#26262a` | page background |
| `--ink` | `#111110` | `#f0f0ee` | headings, strong numbers |
| `--ink-body` | `rgba(17,17,16,.82)` | `rgba(240,240,238,.82)` | body text |
| `--ink-sub` | `rgba(17,17,16,.56)` | `rgba(240,240,238,.56)` | ledes, captions, nav |
| `--ink-weak` | `rgba(17,17,16,.34)` | `rgba(240,240,238,.34)` | labels on labels, dt |
| `--ink-hair` | `rgba(17,17,16,.12)` | `rgba(240,240,238,.15)` | outer borders, section rules |
| `--ink-faint` | `rgba(17,17,16,.06)` | `rgba(240,240,238,.07)` | inner dividers, number chips |
| `--accent` | `#1f4bd8` | `#6b8cff` | card indexes, kickers, focus ring, glitch shadow |
| `--radius` | `0` | `0` | everywhere: `* { border-radius: 0 !important }` |
| `--maxw` | `1280px` | | container width |
| `--container-pad` | `40px` → 24px (≤980) → 16px (≤620) | | side padding |
| `--section-pad` | `128px` → 80px (≤980) | | space above sections |

Fonts: **Inter** 400/500/600 (body), **Inter Tight** 500 (all headings, `--display`), system
mono `ui-monospace, "SF Mono", Menlo, Consolas, monospace` for labels, indexes and numbers.

## 2. Type scale

| Role | Size | Tracking | Notes |
|---|---|---|---|
| Hero name (h1) | `clamp(46px, 9.6vw, 152px)` | -.045em | line-height .98, max 14ch, Inter Tight 500 |
| h2 | `clamp(28px, 3.8vw, 48px)` | -.03em | |
| h3 | `clamp(19px, 2vw, 26px)` | -.015em | |
| Lead | `clamp(17px, 1.5vw, 21px)` | | `--ink-sub`, max 52ch |
| Body | `16px` / 1.55 | | `--ink-body` |
| Caption, buttons, nav | `13px` | .06em, UPPERCASE | |
| Label / eyebrow | `11px` mono | .14em, UPPERCASE | `--ink-sub` (accent for kickers) |

Headings are always `--ink`; body is `--ink-body`; secondary text drops to `--ink-sub`, never
to grey hex values. Numbers (metrics, indexes, table values) are mono or tight display with
`letter-spacing:-.02em`.

## 3. Layout

- Container: `width: min(var(--maxw), 100% - 2 * var(--container-pad)); margin: 0 auto`.
- Topbar: brand left (28px ink square with a white mono "B" + the word "Blaze"), nav right in 13px uppercase `--ink-sub`; `border-bottom:1px solid var(--ink-hair)`.
- Hero: two columns `1.05fr .95fr`, gap 48px, `padding:72px 0`, bottom hairline. Left: eyebrow, h1, lede, CTA row, proof row (big numbers with small captions). Right: a **system card**, a ruled data table instead of an illustration.
- Grids are **joined**, not gapped: `gap:0`, one `1px solid var(--ink-hair)` outer border, inner dividers `1px solid var(--ink-faint)` (`border-right` between cells, `border-bottom` between rows). Last cell drops its divider.
- Metrics strip: `grid-template-columns: 1.3fr repeat(3, 1fr)`, min-height 168px per cell.
- Services: `repeat(3, 1fr)`; showcase: `1.3fr .7fr` (large panel + stack of mini panels).
- Timeline: rows of `64px 1fr`; the number sits in a 40px `--ink-faint` square.
- Section rhythm: `padding-top: var(--section-pad)`, heading block max 680px with a 32px gap below.
- ≤980px: every multi-column grid becomes one column (metrics become 2), nav hides, dividers switch from right to bottom. ≤620px: metrics and services one column, CTA buttons full width.

## 4. Components

- **Eyebrow**: mono 11px, .14em, uppercase, `--ink-sub`, 14px below. The hero eyebrow carries `data-glitch`.
- **Button** (`.btn`): square, `1px solid var(--ink-hair)`, `padding:.7rem 1.3rem`, 13px uppercase .06em, transparent. Hover: inverts to ink background and paper text, and a **particle layer** fades in (`::before` with three `radial-gradient` dot fields of `var(--paper)`, sizes 26/38/18px, drifting via `@keyframes` over 3s linear). `.btn-primary` starts inverted. Wrap the label in `.btn-label` (z-index 1) so dots pass behind it.
- **System card** (hero table): figure with an outer hairline; uppercase 13px caption row; then `dt`/`dd` rows (`--ink-sub` label left, 1.35rem ink value right) divided by `--ink-faint`.
- **Proof row**: `dl` of 2-3 items, big value (1.6rem ink, mono) under a small `--ink-weak` caption, gap 48px.
- **Metric card**: kicker (mono 11px accent uppercase), number `clamp(2rem,3vw,3rem)` mono ink, label 0.9rem `--ink-sub`.
- **Service / product card**: `padding:32px 28px`, index `01` in mono 13px accent, h3, `--ink-sub` paragraph, optional meta line "Live · System prompt · Free" in 13px uppercase.
- **Timeline step**: numbered square, h4 + `--ink-sub` paragraph, rows divided by `--ink-faint`.
- **Partners strip**: centred mono label + a row of uppercase names in `--ink-sub`, .1em tracking.
- **Theme toggle**: fixed 44px square bottom-right, hairline border, sun/moon icon (no words), `aria-pressed`, stored in `localStorage('blaze-theme')`, applied before paint by a tiny inline script.
- **Focus**: `:focus-visible { outline: 2px solid var(--accent); outline-offset: 3px }`. Skip link slides in from the top.
- No shadows, no gradients on surfaces, no pills, no rounded avatars. Images sit in hairline frames.

## 5. Motion

- Reveal: `.js .reveal { opacity:0; transform: translateY(16px); transition: .5s ease }`, made `.visible` once by an IntersectionObserver (threshold .15).
- Glitch eyebrow: every 0.4-1.3s a random comma-separated phrase of the eyebrow jitters for 350ms (`steps(2)` translate) with two clipped copies: top half in `--accent` shifted (-2px,-1px), bottom half in `--ink-sub` shifted (2px,1px).
- Particle dots on button hover (above). The same dot field, static and at low opacity, can texture a hero or empty state.
- Theme switch: 40px tiles flip (`rotateX` 90° → 0 → -90°, .5s, 6ms stagger by diagonal distance) in the new paper colour while every surface transitions colour over .5s underneath; content never disappears.
- Everything respects `prefers-reduced-motion: reduce` (no glitch, no particles drift, no tiles, no smooth scroll, reveals shown at once). Pause loops when offscreen or the tab is hidden.

## 6. Voice

Plain and specific, engineered rather than salesy: "Made for you.", "Shipped from Blaze.",
"Every system we ship is one we run on ourselves, on modest hardware." Short h1s, a one-line
h2 per section, ledes under 40 words, uppercase meta lines with middots.

## 7. Build checklist

1. Copy `assets/tokens.css` and `assets/starter.html`; load Inter 400/500/600 and Inter Tight 400/500/600 with `display=swap`; add the theme-init script in `<head>` before CSS paints.
2. Global reset includes `* { box-sizing:border-box; border-radius:0 !important }`.
3. Only `--accent` is coloured. Check that no other hue slipped in (search the CSS for `#` colours).
4. Build sections from joined, hairline-ruled grids; no floating cards with gaps or shadows.
5. Labels: mono, uppercase, tracked; numbers: mono; headings: Inter Tight 500 with negative tracking.
6. Test light **and** dark (toggle) and both at 360px, 768px, 1280px: borders don't double up, dividers flip to bottom on mobile, nothing overflows.
7. Keyboard: tab through every link and button; focus ring visible in both themes; skip link works.
8. Contrast: `--ink-sub` on paper is ≈ 4.9:1 (fine for text ≥ 13px); never use `--ink-weak` for essential text.
9. Motion: reduced-motion honoured; particle and glitch loops stop offscreen and when hidden; CPU idle when nothing moves.
10. Screenshot both themes and look at them before calling it done.

In the Blaze desktop app the same tokens are the app's CSS variables; reuse them instead of
copying hex values.
