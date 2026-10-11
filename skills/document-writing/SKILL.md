---
name: document-writing
description: Write or edit a document - report, proposal, README, guide, essay, cover letter, SOP, meeting notes - that is clear, structured and ready to share as Markdown, DOCX or PDF. Use for any request to draft, rewrite, summarise or polish long-form text.
tags: [writing, docs, report, markdown, pdf, docx]
version: 1
---

# Document writing

## 1. Pin down the brief (ask only what you can't infer)

- **Reader**: who reads it and what they already know.
- **Purpose**: what the reader should know, decide or do after reading.
- **Form**: type, length, tone (formal/plain/friendly), format (Markdown, DOCX, PDF, Google Doc), deadline.
- **Inputs**: notes, data, links, previous versions, a template or example they like.

## 2. Structure before sentences

Write the outline as headings with one line each, then fill it. Proven shapes:

- **Report / memo**: Summary (the answer in 3-5 lines) → Background → Findings (one heading per finding, evidence under each) → Recommendation → Next steps / owners / dates → Appendix (data, sources).
- **Proposal**: Problem → Goal and success measure → Proposed approach → Scope (in / out) → Timeline → Cost → Risks and mitigations → Decision needed.
- **README**: What it is (1 line) → Screenshot/demo → Install → Quick start (copy-paste) → Usage/config → Troubleshooting → Contributing → License.
- **How-to / SOP**: Purpose → Prerequisites → Numbered steps (one action each, expected result after) → Verification → Rollback → Owner and review date.
- **Meeting notes**: Date, attendees → Decisions → Action items (owner, due) → Discussion highlights → Open questions.
- **Cover letter**: Hook tied to the role → 2 proof points with numbers → Why this company → Call to action. Under 300 words.

## 3. Writing rules

- Lead with the answer. Put the conclusion in the first paragraph; detail follows.
- One idea per paragraph, topic sentence first, 2-4 sentences.
- Short words, active voice, concrete numbers ("cut build time from 41 s to 9.8 s", not "significantly faster").
- Headings are informative ("Costs rise 12% in Q3"), not labels ("Costs").
- Lists for parallel items and steps; tables for comparisons with ≥ 2 attributes.
- Define every acronym once. Remove hedges ("very", "really", "in order to", "it should be noted").
- Keep the user's voice when editing their text; change only what improves clarity, and say what you changed.
- Facts and numbers need a source (link or file); never invent quotes, citations or statistics. Mark unknowns `[TBD: …]`.

## 4. Formats

- **Markdown** (default): `#` title, `##` sections, fenced code, tables, relative links.
- **DOCX**: write Markdown, then `pandoc doc.md -o doc.docx` (add `--reference-doc=ref.docx` for a house style). Or `python-docx` for programmatic documents.
- **PDF**: `pandoc doc.md -o doc.pdf` (needs a LaTeX engine) or HTML → PDF via the browser: write `doc.html` with print CSS (`@page { margin: 20mm }`, 11-12pt serif or Inter, max line ~70 chars) and print to PDF / `chrome --headless --print-to-pdf`.
- Check tools exist first (`pandoc --version`); install_tool if missing and the user agrees.

## 5. Edit pass (do it every time)

1. Read only the headings and first sentences: does the argument hold?
2. Cut 10-20%: repeated points, throat-clearing intros, filler.
3. Check every number, name, date and link.
4. Read it as the target reader: anything unclear or assumed?
5. Spelling and consistency (units, capitalisation, tense, British vs American spelling as the user writes).
6. Save the file, tell the user where it is and offer the next format (PDF/DOCX) if useful.

## Checklist

- [ ] The first paragraph answers the reader's main question.
- [ ] Headings alone tell the story; every section earns its place.
- [ ] Every fact, number and quote has a source; unknowns marked `[TBD]`.
- [ ] Saved in the format the user needs, and they know where it is.
