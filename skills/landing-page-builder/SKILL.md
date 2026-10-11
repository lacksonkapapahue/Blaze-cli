---
name: landing-page-builder
description: Build a single-file landing page (one index.html with inline CSS/JS) that is responsive, accessible and fast - for a product, event, portfolio, waitlist or offer. Use whenever the user wants "a page", "a site", "a landing page" or "a one-pager" and no framework is required.
tags: [web, html, css, landing-page, accessibility, performance]
version: 1
---

# Landing page builder

Goal: one `index.html` that opens by double-click, scores ≥ 90 on Lighthouse for performance,
accessibility, best practices and SEO, and reads well at 360px and at 1920px.
Starter: `assets/template.html` in this skill's folder (semantic skeleton, CSS variables,
reveal-on-scroll, reduced motion, dark mode). Copy it, then fill it.

## 1. Before writing code (2 minutes)

Answer these from the request and memory; ask the user only what you can't infer:
- **Who** is it for, and the **one action** the page wants (buy, join waitlist, book, email).
- **Brand**: existing colours/fonts/logo? If none and the page is Blaze's, load `web-design-blaze-classic`; if the user named a style (e.g. Team of Keys), load that skill.
- **Content** they already have: product name, one-line promise, 3 benefits, proof (numbers, logos, quotes), price, FAQ, contact.
Never invent testimonials, client logos, prices or statistics. Use clearly marked placeholders (`[Quote from a customer]`) and list them in your reply.

## 2. Page structure (pick, don't pad)

1. **Header**: logo/wordmark, 2-4 anchor links, the primary CTA.
2. **Hero**: h1 = the promise in ≤ 8 words; one-sentence subhead (who + outcome); primary CTA + secondary link; a real visual (product screenshot, photo, or a simple SVG), never a stock gradient blob.
3. **Proof strip**: logos or 2-3 numbers (only real ones).
4. **Benefits**: 3 items, each a short title + one sentence, outcome-first ("Ship in a day", not "Fast builds").
5. **How it works**: 3 numbered steps.
6. **Social proof**: 1-3 quotes with name and role.
7. **Pricing or offer** (if any): one clear plan or a 2-3 column comparison, the recommended one marked.
8. **FAQ**: 4-6 real objections, `<details><summary>`.
9. **Final CTA**: repeat the promise and the button.
10. **Footer**: contact, legal links, year.

## 3. Code rules

- Semantic HTML: `header`, `nav`, `main`, `section` with `aria-labelledby`, one `h1`, ordered headings, `footer`. Skip link first.
- CSS in one `<style>`: tokens as custom properties (`--bg --fg --muted --accent --line --radius --max`), fluid type with `clamp()`, layout with grid/flex, `max-width` container ~ 72rem with `padding-inline: clamp(1rem, 4vw, 3rem)`.
- Mobile first; breakpoints only where the layout breaks (usually 48rem and 64rem).
- Images: modern formats, explicit `width`/`height`, `loading="lazy"` and `decoding="async"` below the fold, `alt` that says what matters (empty `alt=""` for decoration). Hero image eager, with `fetchpriority="high"`.
- Fonts: at most 2 families, `display=swap`, preconnect; or a system stack (`system-ui`) when speed matters most.
- JS: optional, < 3 KB, `defer` or at the end; the page must work without it. Use IntersectionObserver for reveals; no scroll listeners doing layout.
- Forms: real `<label>`s, `type="email"`, `autocomplete`, `required`, a visible success/error message with `role="status"`. With no backend, use a `mailto:` link or a Formspree/Netlify-forms action and say so.
- Meta: `<title>` ≤ 60 chars, description ≤ 155, `lang`, viewport, Open Graph title/description/image, favicon (inline SVG is fine).
- Colour contrast ≥ 4.5:1 for text, ≥ 3:1 for large text and UI; visible `:focus-visible`; tap targets ≥ 44px.
- Motion: subtle (opacity + 8-16px translate, 300-600ms), never on essential content; `@media (prefers-reduced-motion: reduce)` disables it. Dark mode via `prefers-color-scheme` if the brand allows.

## 4. Copy rules

Specific beats clever. Lead with the outcome, then the how. One idea per section. Buttons say
what happens ("Join the waitlist", "Book a 15-min call"), never "Submit" or "Learn more" twice.
Cut every sentence that would fit any company.

## 5. Check before you say it's done

- [ ] Open it (open_url or the Design tab) and look at it at ~375px and ~1280px; screenshot if you can.
- [ ] No horizontal scroll at 320px; no text overflowing buttons; images don't stretch.
- [ ] Tab through the page: focus visible, order logical, skip link works.
- [ ] All links and anchors work; CTA goes somewhere real or is flagged as a placeholder.
- [ ] HTML validates (no unclosed tags); no console errors.
- [ ] Total weight: HTML+CSS+JS < 50 KB, images < 300 KB each.
- [ ] List placeholders the user must replace, and how to publish (Netlify drop, GitHub Pages, any static host).
