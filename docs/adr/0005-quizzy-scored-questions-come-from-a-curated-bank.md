# 5. Quizzy scored questions come from a curated bank, never the LLM

Date: 2026-08-04

## Status

Accepted

## Context

Quizzy's Daily Ten questions were invented by the runtime LLM (a 31B model) each
session. Session data showed the effective question pool was only ~30–50 items, so
repeats recurred by birthday-paradox regardless of prompt instructions — negative
constraints ("do not repeat") steer a small model weakly. A 14-day asked-question
ledger and daily category rotation reduced but could not eliminate repeats, and every
generated question risked a hallucinated fact aimed at a young child. Raising
temperature was evaluated and rejected: it mostly rewords the same questions, and the
same completion also emits machine-parsed `MEMO:` state lines, judges answers, and
holds persona — all of which degrade at higher temperature.

## Decision

Scored quiz questions are selected from a human-authored question bank in the
database (`quiz_question`), fetched through a Manager API endpoint. The LLM only
voices the chosen question in persona, judges the child's answer, and encourages. The
LLM may invent **unscored** content only (e.g. the Bonus Buzz question). If the bank
is unreachable, the session offers free chat instead of a quiz — no LLM-invented
scored questions, ever. Questions are organised as Levels: authored sets of ten
within an Age Band (3–5, 6–8, 9+), one Level playable per day, replacing daily
category rotation for Quizzy. Progress is derived from the answer log
(`quiz_question_answer`), not stored counters.

## Consequences

Repeats and hallucinated facts in scored play become impossible by construction, and
per-turn cost drops (fewer generated tokens). In exchange, the team accepts a
content-authoring obligation: banks must stay ahead of the fastest children (the
selection endpoint warns when a device is within ~3 levels of the authored
frontier), and a Manager API outage now means no quiz that day rather than a
degraded one. The category-rotation machinery (`{{TODAY_PLAN}}`) no longer drives
Quizzy question choice; category variety is an authoring guideline within each
Level.
