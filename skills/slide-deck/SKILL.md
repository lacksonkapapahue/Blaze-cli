---
name: slide-deck
description: Make a presentation - pitch deck, class talk, project update or report-out - as a single self-contained HTML deck (arrow keys, fullscreen, print to PDF) or as .pptx when the user needs PowerPoint. Use for any "slides", "deck", "presentation" or "pitch" request.
tags: [slides, presentation, writing, design, pptx]
version: 1
---

# Slide deck

Default output: one `deck.html` built from `assets/deck.html` (16:9 slides, ←/→/space to move,
F for fullscreen, P to print, slide numbers, light/dark, no dependencies). Print it to PDF from
the browser (or `chrome --headless --print-to-pdf=deck.pdf deck.html`). If the user needs an
editable PowerPoint, write a `python-pptx` script instead (section 5).

## 1. Plan the story first

Write the outline before any slide. Ask (or infer) audience, the one thing they should
believe or do afterwards, time slot (≈ 1 slide per minute) and any brand.

Story shapes that work:
- **Pitch**: Problem → Why now → Solution → How it works / demo → Traction → Market → Business model → Competition → Team → Ask.
- **Update**: Headline result → What we did → Numbers vs. goal → What's blocked → Next steps / decisions needed.
- **Teaching**: Hook question → Core idea → 3 parts with examples → Common mistakes → Recap → Exercise.
- **Proposal**: Situation → Complication → Recommendation → Options compared → Cost/timeline → Decision.

Each slide gets an **action title**: a full sentence stating the takeaway ("Churn halved after
onboarding v2"), not a topic ("Churn"). Reading only the titles should tell the whole story.

## 2. Slide rules

- One idea per slide; ≤ 30 words of body text; no paragraphs. Detail goes in speaker notes.
- Big type: title 40-56px, body ≥ 24px at 1920×1080. If it doesn't fit, split the slide.
- One visual per slide: a chart, a screenshot, a diagram, a big number, or a quote.
- Charts: one message, highlighted in the accent colour, everything else grey; label lines directly instead of a legend; start bars at zero; round numbers.
- Consistent grid: same margins (≈ 6% of width), title in the same place on every slide.
- Max 2 fonts, 1 accent colour. For Blaze-branded decks use `web-design-blaze-classic` tokens (paper/ink/#1f4bd8, square edges, mono uppercase labels).
- Never invent data; mark placeholders `[number]` and list them for the user.

## 3. Build with the template

1. Copy `assets/deck.html`. Each slide is a `<section class="slide">`; variants: `.title`, `.big` (one huge number or statement), `.split` (text | visual), `.quote`.
2. Put speaker notes in `<aside class="notes">` inside the slide (hidden; shown with N).
3. Charts: inline SVG (simple bars/lines you compute), or a PNG made with matplotlib (see `data-analysis`).
4. Images: local files next to the deck with `alt` text.

## 4. Review checklist

- [ ] Titles alone tell the story; the first slide says what the talk is for; the last slide says what to do next.
- [ ] No slide over 30 words; no text below 24px; nothing overflows at 1280×720.
- [ ] Contrast ≥ 4.5:1; colour isn't the only way information is shown.
- [ ] Numbers are sourced (footnote small on the slide or in notes).
- [ ] Open it, step through every slide, print preview to PDF once.
- [ ] Time it: slides ≈ minutes; cut rather than rush.

## 5. PowerPoint (.pptx)

```python
from pptx import Presentation
from pptx.util import Inches, Pt
prs = Presentation(); prs.slide_width, prs.slide_height = Inches(13.333), Inches(7.5)
s = prs.slides.add_slide(prs.slide_layouts[5])          # title only
s.shapes.title.text = "Churn halved after onboarding v2"
tb = s.shapes.add_textbox(Inches(0.8), Inches(1.8), Inches(11.7), Inches(4)).text_frame
tb.text = "Monthly churn 6.1% → 3.0% (Jun-Sep)"; tb.paragraphs[0].font.size = Pt(28)
s.notes_slide.notes_text_frame.text = "Speaker notes here"
prs.save("deck.pptx")
```
Install with `pip install python-pptx` (ask first). Use `add_picture` for charts rendered to PNG.
Keep the same rules: action titles, one idea, big type.
