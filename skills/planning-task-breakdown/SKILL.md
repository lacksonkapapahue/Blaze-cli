---
name: planning-task-breakdown
description: Turn a goal or a big messy request into a concrete plan - milestones, ordered tasks with owners and estimates, dependencies, risks and the next action - and keep it updated as work happens. Use for projects, launches, study plans, "how do I start", multi-step coding work, or before any task that will take more than ~30 minutes.
tags: [planning, tasks, project-management, productivity]
version: 1
---

# Planning and task breakdown

## 1. Define done

- Goal in one sentence, with a measurable finish line and a date ("Store live with 20 products and payments working by 1 Nov").
- Constraints: time per week, budget, tools/accounts available, skills, hard deadlines.
- Out of scope: write it down; it prevents creep.
- Ask only what changes the plan (usually deadline and budget/time); infer the rest from memory and context.

## 2. Break it down

- Milestones: 3-6 outcomes that each prove progress (not activities). Order them so the riskiest/most uncertain comes early.
- Tasks under each milestone: verb-first, concrete, ≤ 1 day each ("Write product descriptions for 5 lamps"), each with a clear "done" check. If a task can't be estimated, the first task is a spike to find out.
- Dependencies: mark what blocks what; find the critical path.
- Estimates: hours or S/M/L; add 30-50% buffer for anything new to the user.
- First action: something doable in the next 15 minutes.

## 3. Write the plan

Use a checklist the user (and you) can tick:

```markdown
# Plan: <goal> - by <date>
Done when: <measurable finish line>

## Milestone 1 - <outcome> (target <date>)
- [ ] Task (est. 2h) - done when …
- [ ] Task (est. S) - blocked by …

## Risks
- <risk> → <mitigation / early check>

## Next action
- <15-minute step>
```

Save it as `PLAN.md` in the project (or the project's notes) so it persists; for coding work also
mirror it in the todo list tool if available.

## 4. While working

- Update the plan as you go: tick tasks, add discovered ones, move dates honestly.
- One task in progress at a time; finish → verify → tick.
- When blocked, say what's blocking and propose the smallest unblocking step.
- Re-plan when reality diverges by > 25%: cut scope before moving the deadline (or ask which matters more).

## 5. Good plans

- [ ] Every task has a done-check; nothing vague ("work on marketing").
- [ ] Riskiest assumption tested early and cheaply.
- [ ] Fits the user's real available time.
- [ ] Includes testing, review and launch/hand-off tasks, not just building.
- [ ] Recurring workflows discovered while planning are good candidates for a skill.
