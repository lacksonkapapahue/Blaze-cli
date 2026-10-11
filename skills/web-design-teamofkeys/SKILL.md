---
name: web-design-teamofkeys
description: Design or build a website in the style of teamofkeys.com - near-black and white sections, one hot orange accent, huge tight sans headlines with one italic serif word, mono "(labels)", pill CTAs and slow scroll-driven motion. Use when the user asks for that look, a studio/agency feel, or names Team of Keys.
tags: [design, web, css, landing-page, agency, motion]
version: 1
---

# Web design: the Team of Keys look

Studied from https://teamofkeys.com/ (its HTML, `css/styles.css` and `js/main.js`). A confident
studio site: alternating **ink** and **paper** sections, one **orange** accent used sparingly,
display type set very large and very tight, one *italic serif* word in each headline, small
mono labels in parentheses, generous gutters and slow, eased, scroll-linked motion.

Starter files in this skill's folder: `assets/tokens.css` (every token plus the base
components) and `assets/starter.html` (hero, statement, services accordion, stats, steps,
contact, footer). Copy both into the project, then adapt the copy and the sections.

## 1. Tokens

| Token | Value | Use |
|---|---|---|
| `--ink` | `#050505` | default page and dark sections |
| `--paper` | `#FFFFFF` | light sections, text on ink |
| `--paper-dim` | `rgba(255,255,255,.72)` | secondary text on ink |
| `--ink-dim` | `rgba(5,5,5,.66)` | secondary text on paper |
| `--orange` | `#FF6428` | the one accent: italic word, CTA, progress bars, ::selection |
| `--orange-deep` | `#D94E18` | pressed or shadow side of orange |
| `--stone` | `#E4DED3` | rare warm neutral tile |
| `--line-on-ink` | `rgba(255,255,255,.16)` | hairlines on dark |
| `--line-on-paper` | `rgba(5,5,5,.16)` | hairlines on light |
| surfaces | `#000`, `#141414`, `#1f1f1f` | stats band, cards, dark buttons |
| `--gutter` | `clamp(1.25rem, 4vw, 4.5rem)` | page side padding |
| `--section-y` | `clamp(4.5rem, 9vw, 8.5rem)` | section top and bottom |
| `--nav-h` | `4.5rem` | fixed nav height |
| `--ease` | `cubic-bezier(.2,.7,.2,1)` | every CSS transition |

Fonts (Google Fonts; self-host in production): **Instrument Sans** 400/500/600 for everything,
**Instrument Serif** italic 400 for the one accent word, **DM Mono** 400/500 for labels, CTAs
and form labels.

## 2. Type scale

| Role | Size | Line height | Tracking | Weight |
|---|---|---|---|---|
| Hero title | `clamp(3rem, 7.6vw, 7.6rem)`, max 12ch | .92 | -.06em | 400 |
| Section h2 | `clamp(2.2rem, 5.2vw, 5.2rem)` | .98 | -.035em | 500 |
| Contact title | `clamp(2.6rem, 6.4vw, 6.5rem)` | .94 | -.05em | 500 |
| Statement paragraph | `clamp(1.7rem, 3.1vw, 3.3rem)`, max 34ch, `text-indent: 3.4em` | 1.14 | -.035em | 500 |
| Card or step title | `clamp(1.1rem, 1.6vw, 1.5rem)`, open: `clamp(1.9rem, 3.4vw, 3.2rem)` | 1.05 | -.03em | 500 |
| Body | `1.0625rem` | 1.55 | normal | 400 |
| Mono label | `.78rem` DM Mono | 1.6 | .01em | 400 |

Rules:
- Every big headline has **one** word in `<em>`: Instrument Serif italic 400, `1.04-1.06em`, `letter-spacing:-.02em`, colour `--orange` ("Different minds. One *powerful* combination.").
- Section labels are mono and often in parentheses: `(Selected work)`, `(What sets us apart)`, `(Scroll)`, coloured with the dim foreground.
- Numbers are mono and zero-padded: `01`, `02 / 11`, `(003)`.
- Headlines break into deliberate lines (one `<span class="line">` per line), never a ragged auto-wrap.

## 3. Layout and grid

- Full-bleed sections with content inset by `--gutter`, no centred 1200px box; wide screens just get more air.
- Section heads: label above the h2. Split variant `grid-template-columns: 1fr 2fr; align-items: end` (label left, h2 right).
- Hero: `min-height:100svh; display:grid; grid-template-rows:auto 1fr auto`: intro line on top (second half dimmed), title bottom-left, a services line and scroll cue at the bottom.
- Sections alternate `.section--ink` and `.section--paper`; each sets `--fg`, `--fg-dim` and `--line` so components adapt.
- Overlap transition: the next dark section slides up over the previous one with `margin-top:-14vh; border-radius:2.5rem 2.5rem 0 0` (or a curved `50% 50% 0 0 / 16vh` top).
- Lists of three: `repeat(3, 1fr)` with `gap:2rem`, each item with a top hairline and `padding-top:1.5rem`.
- Contact: `minmax(0,1.3fr) minmax(0,.7fr)` (form, then a sticky "What happens next" card).
- Breakpoints: 900px (stack the splits, 2-column stats), 760px (single column, smaller hero), 560px.

## 4. Components

- **Nav**: fixed and transparent at the top. After scrolling, a `::before` layer fades in with `backdrop-filter: blur(14px) saturate(1.2)`, `rgba(5,5,5,.72)` and an inset bottom hairline. It recolours per section (`html[data-nav="light"|"orange"]`). Brand: a small boxed uppercase wordmark with a .36rem orange square on its corner. Right side: orange CTA pill and a round menu button.
- **CTA pill**: `border-radius:999px`, orange background, ink text, DM Mono `.78rem`, padding `.45rem .5rem .45rem 1.15rem`, ending in a 1.9rem round ink **badge** with an ↗. Hover: the pill turns paper, the badge turns orange and `rotate(45deg) scale(1.06)`; active `scale(.97)`.
- **Outline button**: pill, `1px solid currentColor`, `.95rem 1.6rem`; hover fills orange (on ink) or ink (on paper).
- **Full-screen menu**: numbered links (`01 Expertise ↗`), a side column "(What we build)" listing services, contact details at the bottom.
- **Services accordion**: numbered rows with a top hairline; the body opens with `grid-template-rows: 0fr → 1fr` over `.55s var(--ease)`; inside are a paragraph, three bullets and a mono "Enquire about X" link.
- **Stat cards**: four columns on `#000`, `border-radius:10px`. The `#141414` front has a big number (`clamp(2.8rem,4.2vw,3.75rem)`) and caption; hover or focus flips it (`rotateY(180deg)`, `.85s cubic-bezier(.44,0,.1,1)`) to a white back with a quote and ★★★★★. On desktop the cards are sticky and staggered (`margin-top: calc(var(--i) * 12.5rem)`).
- **Process steps**: four horizontal panels, `border-radius:1.5rem`, inset hairline. The open one grows (`flex-grow:3.4`), gets a warm radial glow and an orange 2px top rail that scales in, and its text fades up. They stack under 900px.
- **Work strip**: a horizontal scroll-snap track of tiles (image, `01`, title, "next.js · 2025"); on desktop it is pinned and driven by scroll with a thin orange progress bar.
- **Logo wall**: greyscale client logos on keycap-like tiles that press down .28rem and regain colour on hover.
- **FAQ**: question rows with the same grid-rows accordion.
- **Contact form**: no boxes. Inputs have only `border-bottom:1px solid var(--line)`, labels are mono, focus turns the line orange with a soft orange underglow, and the custom select is a dark list panel (`border-radius:.9rem`). Send is an orange pill with a badge. A large orange radial glow sits bottom-left of the section.
- **"What happens next" card**: glass (`linear-gradient(165deg, rgba(255,255,255,.07), rgba(255,255,255,.025))`, blur 20px, `border-radius:1.5rem`), numbered round steps joined by a 1px line, the first number filled orange.
- **Footer**: ink; a brand and tagline row, then a meta row under a hairline in dim text.
- `::selection { background: var(--orange); color: var(--ink) }`.

## 5. Motion and interaction

- The original uses Lenis smooth scrolling and GSAP ScrollTrigger; plain CSS and an IntersectionObserver cover most builds (the starter does this).
- Default reveal: headline lines rise out of a mask (`.line{overflow:hidden}` > span, `translateY(110%) → 0`), about 1s, ease like GSAP `power3.out` (≈ `cubic-bezier(.22,.61,.36,1)`), stagger .12s.
- Statement paragraphs reveal word by word (each word in an overflow-hidden inline-block).
- Pinned sections follow scroll position (scrub); no autoplaying loops.
- Hovers are slow and soft (.3-.55s, `var(--ease)`); presses are quick (.08s).
- Keyboard: interactive tiles are focusable and show a 2px orange `:focus-visible` ring (the original also maps keys 1-9 to its services).
- Pause decorative loops offscreen (IntersectionObserver) and when the tab is hidden (`visibilitychange` → `html.motion-paused`).
- `prefers-reduced-motion: reduce`: no smooth scroll and no pins, reveals shown at once, flips become fades.

## 6. Voice

Short declaratives in two beats ("Different expertise. Extraordinary together."), plain promises
("You get a plan with a price per phase - usually within one working day."), mono labels in
parentheses, no exclamation marks.

## 7. Build checklist

1. Copy `assets/tokens.css` and `assets/starter.html`; load the three fonts with `display=swap`.
2. Sections in order: hero → statement → services → work → stats/testimonials → pillars or people → process (four steps) → FAQ → contact → footer. Drop what the content doesn't need.
3. Alternate ink and paper; at most two orange elements visible at once (italic word and CTA).
4. Each h2: mono label above, one italic orange word, hand-placed line breaks.
5. Hero title ≤ 12ch per line. Check 360, 768, 1280 and 1920px wide: no word overflows (`overflow-wrap:anywhere` on long words).
6. Every transition uses `var(--ease)`; reveals run once; with JS off all content is visible.
7. Accessibility: skip link, real `<button>`s with `aria-expanded` for accordions, a label for every input, contrast ≥ 4.5:1, visible focus.
8. Performance: `.webp` images with width and height, `loading="lazy"` below the fold, preloaded fonts, no layout shift; Lighthouse ≥ 90.
9. Reduced motion and the offscreen pause both verified.
10. Screenshot ink and paper sections at desktop and phone widths and look at them before calling it done.

For the Blaze brand (square edges, blue accent) use `web-design-blaze-classic` instead: the
pills and orange belong to Team of Keys, not Blaze.
