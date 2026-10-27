# ADR-0002: Measure attention, not active-window time

**Status:** Proposed; presence amended by
[ADR-0008](0008-retroactive-idle-resolution.md) (idle stretches decided
after they end, and how calls and video are detected on macOS)
**Date:** 2026-10-20
**Deciders:** Project maintainers

## Context

Timing the active (focused) window misattributes time on multi-monitor setups.
Users read docs, Claude Code output or a call on one screen while focus sits
on another. Idle detection based only on input marks reading and watching as
"away". The opposite error is just as bad: background music or a forgotten
video shouldn't count as presence.

## Decision

Model attention as a probability distribution over visible windows, updated
every 1 s tick.

**Signals.** Each signal is normalised to [0, 1] with a saturating
function, so the weights don't depend on how fast someone types:
- focus;
- keyboard activity counts;
- scroll and click location;
- pointer hover dwell;
- window visibility and occlusion;
- media and call state;
- lock state.

A softmax with an explicit temperature turns the scores into a
distribution.

**Presence** decays over an app-aware grace period. The period is longer
for a terminal with fresh agent output and for documents.
- **Audio-only media never holds presence.** Only video playing in a
  *visible* window, or an active call (microphone in use), keeps presence
  at 1.0.
- When no window's signal clears a threshold, presence goes to an
  **`unattributed`** bucket instead of being forced onto some window. That
  covers someone reading paper or looking at a phone.

**Accessibility input profile.** Keyboard-only, voice control, switch
access and screen-reader users have different input patterns. The profile
adjusts the signal weights and grace periods so presence detection doesn't
penalise them. Each mode is covered by tests.

**Persistence.** Store **per-minute rollups per window** (`attended_s`,
`focused_s`, `visible_s`), and report on `attended_s`.
- 1 Hz ticks and pointer positions are never persisted.
- Classification (ADR-0006) happens before anything is written.

**Input counting without keylogger mechanics:**
- **Windows:** Raw Input (`RegisterRawInputDevices` with `RIDEV_INPUTSINK`)
  on a dedicated locked OS thread. Not low-level hooks, which add latency to
  all system input and get removed silently when slow.
- **macOS:** a listen-only `CGEventTap` on a dedicated thread. Its callback
  only increments a counter.
- Key codes are never read. Both callbacks look only at the event *type*
  and never call key-code accessors (`CGEventGetIntegerValueField` for
  keycodes, `RAWKEYBOARD` fields). The callback sits in one small package
  with a lint rule that forbids those calls. Input Monitoring stays optional.

**Cadence and energy:**

| Signal | Cadence |
|--------|---------|
| Focus | event-driven |
| Window geometry | event-driven (AXObserver / `EVENT_OBJECT_LOCATIONCHANGE`, filtered and debounced) + 5 s reconcile |
| Pointer | 1 Hz |
| Idle | 5 s |

- All sampling pauses while locked or idle and slows down on battery.
- On macOS the sampling thread opts out of App Nap
  (`NSProcessInfo.beginActivity`), so ticks aren't throttled.
- Budget: ≤ 2 wakeups/s on average; a zero-allocation steady-state tick.

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
| Privacy | Uses positions and counts in memory only; persists per-minute rollups |
| Energy | Event-driven with low-rate sampling; App Nap and battery aware |

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
no privacy concerns. Normalised, saturating signals and the `unattributed`
bucket keep the model honest at the edges: fast typists, music left
playing, paper reading. Keeping it a pure function over recorded event
traces makes it testable and lets us swap the weights for a fitted model
later.

## Consequences

- Easier: correct accounting on multi-monitor setups, reading time, and the
  human timeline for agent sessions (ADR-0003).
- Harder: more signals per OS; Wayland needs compositor bridges (ADR-0001
  components); the accessibility profile needs real-user testing.
- Revisit: fit the weights and temperature from spot-labels and check-ins in
  Phase 2.

## Action Items
1. [ ] `internal/attention` as a pure package (injected `Clock`, no goroutines) with trace-replay tests.
2. [ ] Property tests: attention ≥ 0 per window; Σ attention ≤ presence ≤ 1; rollups sum to the same totals as ticks.
3. [ ] Record and annotate golden traces: 2-monitor, background audio, video call, paper reading (unattributed), keyboard-only.
4. [ ] Windows Raw Input collector; macOS counter-only event tap.
5. [ ] Per-collector cadence with lock/idle/battery throttling; App Nap opt-out; wakeups/s and allocation benchmarks.
6. [ ] GNOME Shell extension + KWin script over a private peer-to-peer channel (no public session-bus service), returning only the fields we need.
