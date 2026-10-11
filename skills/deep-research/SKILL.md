---
name: deep-research
description: Research a question thoroughly on the web - compare options, check facts, survey a topic, find the best tool/product/price - and answer with sources for every claim. Use when the answer needs more than one search, the stakes are real, or the user asks for "research", "compare", "find out", "is it true that".
tags: [research, web, sources, fact-check, comparison]
version: 1
---

# Deep web research with sources

## 1. Frame it

- Restate the question as one sentence plus the decision it feeds ("Which free STT model runs offline on an 8 GB laptop - for Blaze's voice input").
- List what would change the answer: constraints (budget, region, OS, date range, language), must-haves vs. nice-to-haves.
- Write 3-6 sub-questions. Note what you already know and how sure you are.
- Recall memory first (`recall`) for the user's stack, location, past choices.

## 2. Search well

- Use `web_search` with specific queries: product + version, exact error strings in quotes, `site:` (docs, github.com, reddit.com, news sites), year for freshness ("2026").
- Vary angles: official docs/spec, independent benchmark or review, community experience (issues, forums), recent news/changelog.
- Open the pages (`fetch_page`) before relying on them; a search snippet is not a source. Read the relevant section, note the date.
- Prefer primary sources: official docs, pricing pages, papers, filings, the repo itself, government sites. Treat SEO listicles and affiliate "best X" pages as leads, not evidence.
- Stop when the facts that could change the conclusion are found and corroborated by two independent sources; don't pad with more searches.

## 3. Weigh evidence

For each key claim record: claim · source URL · date · type (primary/secondary/opinion) · confidence.
- Conflicts: say so, prefer the newer and the primary source, explain the difference.
- Numbers: check units, dates and what exactly was measured.
- Time-sensitive facts (prices, versions, availability, laws, rankings) need a date in the answer.
- Note gaps plainly ("No independent benchmark found for X").

## 4. Answer

Shape:
1. **Answer first** - the recommendation or finding in 1-3 sentences, with your confidence.
2. **Why** - the 3-5 facts that drive it, each with an inline source link.
3. **Comparison table** when there are options (columns = what matters to this user).
4. **Caveats / what would change it**.
5. **Sources** - list of URLs with one-line descriptions and dates.

Rules: never cite a page you didn't open; never invent URLs; quote sparingly and exactly;
distinguish fact from your judgment ("I'd pick X because…"). Save long research to a Markdown
file in the project (e.g. `research/<topic>.md`) and summarise in chat.

## 5. Checklist

- [ ] Each sub-question answered or marked unknown.
- [ ] Every non-obvious claim has a source opened this session.
- [ ] Dates on prices/versions; currency and region correct for the user.
- [ ] The answer fits the user's constraints (a filter, not a suggestion).
- [ ] Durable findings that matter later (e.g. the tool the user chose) saved with `remember`.
