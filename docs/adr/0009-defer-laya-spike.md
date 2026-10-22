# ADR-0009: Defer the Laya spike to Phase 2

**Status:** Accepted
**Date:** 2026-10-22
**Deciders:** Project maintainers

## Context

ADR-0006 makes Laya an **optional, local activity classifier**: step 4 of
the classification cascade, never the scorer. Rules plus a small embedding
kNN (its Option D) are the fallback and the benchmark baseline. The
roadmap ships the optional Laya classifier in Phase 2, but it put a **Laya
spike** in Phase 0, with "Laya go/no-go" as an exit criterion. The spike
would hand-label about 500 samples across four roles and compare rules,
kNN and Laya on accuracy, CPU latency and memory.

Phase 0 is now otherwise done, and the spike doesn't fit it:

- **There is nothing to label yet.**
  - Phase 0 has one user, a developer. The spike needs samples from
    students, researchers and PMs, who arrive with the Phase 1 alpha (10–20
    mixed-role users).
  - Labelling one person's activity would measure the wrong thing.
- **Laya's value depends on window titles.**
  - It earns its place classifying titles and domains, where browser-heavy
    roles live (ADR-0006).
  - Titles are off by default. They need an opt-in, the Screen Recording
    permission on macOS, and a redactor, and none of that exists yet.
  - On app ids alone, which is all Phase 0 records, rules already do the
    job.
- **The baseline isn't measured.** Rules are Option D's first half and the
  bar Laya must clear. Their accuracy is unknown until the Phase 0
  measured day and the alpha. Comparing against an unmeasured baseline
  wouldn't support a decision.
- **The cost is high and the model is young.**
  - Laya means a second binary, ONNX Runtime per OS, per-OS sandboxes, and
    a 340 MB–1.7 GB model download.
  - Laya was released in September 2026, and its public specs conflict
    (context length, sizes). Waiting a phase costs nothing and lowers the
    risk.

## Decision

- **Phase 0's Laya go/no-go is "not now."** The spike moves to Phase 2 and
  stops being a Phase 0 exit criterion.
- **Phase 1 builds what the spike needs**, as part of work it already
  plans:
  - role profiles and rules;
  - the redactor (ADR-0006 action item 5);
  - opt-in spot labels from alpha users.
- **Phase 2 runs the spike** against that labelled set: rules vs kNN vs
  Laya. Laya ships only if it is clearly better than Option D, as ADR-0006
  already says.
- **ADR-0006 stays the target design.** Only its timing changes.

## Consequences

- Phase 0 exits on the measured-day accuracy test alone, with the privacy
  canary and ps-leak tests already passing.
- Until Phase 2, classification is rules and user overrides. Browser-heavy
  roles see more `browser` and `uncategorized` time than they would with a
  classifier. Their alpha feedback is what the spike will measure against.
- No ONNX, sidecar or model-download work lands before it has a measured
  reason to.

## Revisit when

- A labelled set of at least 500 samples across at least three roles
  exists.
- Alpha users report that their work is poorly classified, notably in the
  browser.
- Laya ships an official small CPU build, or a pure-Go runtime becomes
  viable (ADR-0006's own revisit trigger).
