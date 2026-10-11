---
name: web-audit
description: Audit a website or web app for accessibility, performance, SEO and basic security, then fix the top issues - with Lighthouse, axe, browser devtools and manual keyboard/screen-reader checks. Use when a page is slow, before launch, when the user asks "is my site good/accessible/fast", or after building a page.
tags: [web, accessibility, performance, seo, lighthouse, audit]
version: 1
---

# Web audit: accessibility, performance, SEO

## 1. Measure (before changing anything)

- Lighthouse (mobile preset): `npx lighthouse https://site --preset=desktop|perf --output=html --output-path=report.html` or Chrome DevTools › Lighthouse. Run 3 times; use the median.
- Real-user data if available: PageSpeed Insights (CrUX field data) for public sites.
- Accessibility engine: `npx @axe-core/cli https://site` (or the axe DevTools extension).
- Note the baseline: LCP, INP (or TBT), CLS, total transfer size, request count, a11y/SEO scores.
- Local pages: serve them (`npx serve .` or `python -m http.server`) - file:// skews results.

Targets: LCP < 2.5 s, INP < 200 ms, CLS < 0.1, page weight < 1 MB (landing pages < 500 KB), Lighthouse ≥ 90 in every category.

## 2. Accessibility (WCAG 2.2 AA essentials)

Automated tools find ~30-40%; always do the manual pass:
- [ ] Keyboard only: Tab through everything; visible focus; logical order; no traps; Esc closes dialogs; skip link.
- [ ] Every image has meaningful `alt` (or `alt=""` if decorative); icons-only buttons have `aria-label`.
- [ ] One `h1`; headings in order; landmarks (`header`, `nav`, `main`, `footer`).
- [ ] Form fields have `<label>`s; errors are announced (`aria-live`/`role="alert"`) and described in text, not colour only.
- [ ] Contrast ≥ 4.5:1 text, ≥ 3:1 large text and UI components (check hover/focus/disabled states and dark mode).
- [ ] Target size ≥ 24×24 px (44 recommended on touch).
- [ ] Zoom to 200% and 400% (reflow at 320px wide) without loss of content.
- [ ] `prefers-reduced-motion` respected; nothing flashes > 3×/s; autoplaying media can be paused.
- [ ] `lang` on `<html>`; page `<title>` unique and descriptive.
- [ ] Screen reader spot-check (NVDA on Windows, VoiceOver on macOS: Cmd+F5): page title, headings list, a form, a dialog.

## 3. Performance fixes, in order of payoff

1. **Images**: right size (≤ 2× display), WebP/AVIF, `width`/`height` set (CLS), `loading="lazy"` below the fold, hero `fetchpriority="high"`, no lazy-loading the LCP image.
2. **Render-blocking**: inline critical CSS or keep CSS small; `defer` scripts; remove unused JS/CSS (DevTools › Coverage); avoid large frameworks for static pages.
3. **Fonts**: ≤ 2 families, `font-display: swap`, preload the main weight, subset; or system fonts.
4. **Third parties**: remove or delay chat widgets, trackers, embeds (facade for YouTube/maps).
5. **Caching/compression**: long `Cache-Control` for hashed assets, Brotli/gzip, HTTP/2+, a CDN.
6. **JS work**: break long tasks, debounce input handlers, avoid layout thrash, pause animations offscreen.
7. **Server**: TTFB < 800 ms; cache HTML where possible.

## 4. SEO basics

`<title>` ≤ 60 chars and meta description ≤ 155 per page; one h1; descriptive link text; canonical URL; `robots.txt` and `sitemap.xml`; Open Graph/Twitter tags with a 1200×630 image; structured data (JSON-LD) for products/articles/organisation/FAQ when relevant; clean URLs; no broken links (`npx linkinator https://site`); mobile-friendly; HTTPS.

## 5. Security quick check

HTTPS with HSTS; no mixed content; security headers (`Content-Security-Policy`, `X-Content-Type-Options: nosniff`, `Referrer-Policy`, `frame-ancestors`/`X-Frame-Options`); forms protected from spam (honeypot/captcha) and CSRF where there are sessions; no secrets or API keys in client JS; dependencies without known vulnerabilities (`npm audit`).

## 6. Report

Lead with the scores before/after and the 3-5 biggest issues, each with impact, evidence (metric or screenshot) and the fix. Fix what you can directly (with the user's go-ahead for anything structural), re-measure, and list what remains with effort estimates.
