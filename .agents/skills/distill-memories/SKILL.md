---
name: distill-memories
description: Read `.agents/docs/JOURNAL.md` and `.agents/docs/LTM/` documents, find durable knowledge that belongs in the canonical docs, and update those documents with concise synthesized findings. Targets are `.agents/docs/OVERVIEW.md` and the root `ARCHITECTURE.md`. Use when journal or LTM notes have accumulated implementation, system, or user-facing knowledge that should be promoted into the project's canonical overview or architecture docs.
user-invocable: true
allowed-tools: Bash, Read, Write, Edit, Grep, Glob
---

# Distill Memories

## Overview

Take the durable facts in `.agents/docs/JOURNAL.md` and `.agents/docs/LTM/` and move them into the project's canonical documentation. Keep these two documents up to date:

a. `.agents/docs/OVERVIEW.md` ... high-level system understanding for coding agents: goals, decisions already made, milestone map.
b. `ARCHITECTURE.md` (repository root) ... structure, subsystems, and technical design. It is human-reader-ready and canonical. Its "Using Oganesson" section covers user-facing behavior until a separate docs tree exists: CLI commands and flags, `og.config.*` fields, built-in `og:*` modules, and supported and unsupported Next.js features.

## Sources: JOURNAL and LTM

- `.agents/docs/JOURNAL.md` is the append-only log of findings, insights, and code-review history. It holds the **freshest** durable facts, including changes not yet consolidated into LTM. It starts with the M0 to M5 milestone records. The `good-sleep` and `reconcile-journal-ltm` skills consolidate JOURNAL entries into `.agents/docs/LTM/`; this skill runs against whatever is present.
- `.agents/docs/LTM/` holds the consolidated, topic-organized reference material. `INDEX.md` is its table of contents.

Read both. Where JOURNAL and LTM overlap, treat LTM as the settled synthesis and JOURNAL as the source of anything newer than the last consolidation. A JOURNAL entry recording a substantial change that the target docs do not yet reflect is exactly the gap this skill closes.

The source of truth for user-facing detail is always the code. Look at:

- `cmd/og/*.go`;
- `internal/jshost/plugins.go` and `internal/jshost/js/bridge.js` for `og.config.*`;
- `runtime/kv/kv.d.ts`;
- and similar.

Verify a fact against the code before writing it down. JOURNAL and LTM tell you what changed, not the exact current flag or field name.

## Read in This Order

1. `.agents/docs/OVERVIEW.md` and the root `ARCHITECTURE.md`.
2. `.agents/docs/JOURNAL.md` (at least the entries since the last consolidation record) and `.agents/docs/LTM/INDEX.md`.
3. Only the LTM documents relevant to the gaps, stale sections, or missing detail you found in the target docs.

## Classify Findings Before Editing

For each candidate fact, decide where it belongs:

- `OVERVIEW.md`: product scope, decisions, milestone state, orientation material.
- `ARCHITECTURE.md`:
  - module layout, subsystem boundaries, build and runtime data flow, interface contracts (headers, manifests, built-in modules), and testing constraints;
  - user-facing behavior goes in its "Using Oganesson" section.

  Favor narrative and durable design rationale over implementation trivia.
- Both, when a change affects orientation and design alike.
- Neither: narrow bug history, one-off workarounds, details too fine-grained for canonical docs. Leave these in LTM.

Prefer durable knowledge over incident history. Convert timelines into timeless guidance.

## Update Strategy

- Synthesize; do not copy JOURNAL or LTM prose verbatim.
- Merge into existing sections where possible. Add a section only for a stable topic the document truly lacks.
- Keep it compact. The core docs should be easier to scan than the notes behind them.
- Preserve exact file paths, component names, header names, flags and fields where they add precision.
- If sources disagree, or JOURNAL disagrees with the code, call out the ambiguity or ask the user before cementing one reading.

## Validation

1. Re-read the edited sections.
2. Check that each added fact is supported by a JOURNAL or LTM note and, for user-facing detail, by the code.
3. Check that each fact sits at the right level: orientation material in OVERVIEW, design detail in ARCHITECTURE.
4. Keep the documentation style rules in `AGENTS.md`: half-width parentheses and half-width colons.
5. Check that every relative link you added resolves to an existing file.
