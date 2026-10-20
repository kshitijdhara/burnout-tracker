# ADR-0002: Measure attention, not active-window time

**Status:** Proposed
**Date:** 2026-10-20
**Deciders:** Project maintainers

## Context

Timing the active (focused) window misattributes time on multi-monitor setups.
Users read docs, Claude Code output or a call on one screen while focus sits
on another. Idle detection based only on input marks reading and watching as
"away".

## Decision

Model attention as a probability distribution over visible windows, updated
at 1 Hz. The inputs are focus, keyboard activity counts, scroll/click
location, pointer hover dwell, window visibility/occlusion, media/call state
and lock state. Presence decays over an app-aware grace period (longer for
terminals with fresh agent output, documents and media). Store
`attended_s`, `focused_s` and `visible_s` per interval, and report on
`attended_s`.

## Options Considered

### Option A: Focused-window time (status quo in most trackers)
| Dimension | Assessment |
|-----------|------------|
| Complexity | Low |
| Accuracy | Poor on multi-monitor; poor for reading |

### Option B: Heuristic attention distribution (chosen)
| Dimension | Assessment |
|-----------|------------|
| Complexity | Medium |
| Accuracy | Good; explainable; tunable |
| Privacy | Uses positions and counts only |

### Option C: Webcam gaze tracking
| Dimension | Assessment |
|-----------|------------|
| Complexity | High |
| Accuracy | Best |
| Privacy | Unacceptable as a default; battery cost |

### Option D: Learned attention model
Not possible without labels. It becomes possible once we collect spot-labels
("what are you looking at?" prompts, opt-in), so it is the natural successor
to B.

## Trade-off Analysis

B captures most of the gap between A and C at a fraction of the cost and with
no privacy concerns. Keeping it a pure function over recorded event traces
makes it testable and lets us replace the weights with a fitted model later.

## Consequences

- Easier: correct accounting for multi-monitor setups, reading time, and the
  human-segment split for agent sessions.
- Harder: more signals per OS. Wayland needs compositor extensions.
- Revisit: fit the K weights from spot-labels and check-ins in Phase 2.

## Action Items
1. [ ] `internal/attention` as a pure package with trace-replay tests.
2. [ ] Record and annotate a 2-monitor golden trace for regression tests.
3. [ ] GNOME Shell extension exposing window list + pointer over D-Bus.
