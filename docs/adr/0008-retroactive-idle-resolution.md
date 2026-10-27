# ADR-0008: Decide idle stretches after they end

**Status:** Accepted
**Date:** 2026-10-21
**Deciders:** Project maintainers

## Context

ADR-0002 keeps presence at 1 for 30 s after the last input, then decays it
over a fixed, app-aware grace window (1–3 min). The first real-machine test
showed that this undercounts badly:
- **Reading:** reading an article for a few minutes without scrolling went
  missing.
- **Meetings:** a 30-minute Google Meet call went missing, because call and
  video detection wasn't built on macOS yet.

A longer fixed grace only trades undercounting for overcounting:
- Every coffee break would gain minutes of "presence".
- Without a camera, which we rule out, the idle seconds themselves can't
  tell reading from absence.

How an idle stretch **ends** can tell them apart:
- **The user's own input** ends it soon after: they were at the screen,
  reading or thinking.
- **A lock, display sleep or system sleep** ends it: they had left.

## Decision

The engine still computes the grace-rule presence for every tick. Ticks in
an idle stretch are also marked idle. While the stretch can still be
confirmed, they also carry the attention distribution the user would have
if present. A pure `attention.Resolver` holds those ticks until the stretch
ends, then releases them in order:

- **Confirmed:** the user's own input ends the stretch, the stretch was
  contiguous (no sleep gap), and it is still within its cap. Every tick
  counts as fully present, with reason `confirmed_idle` (reading or
  thinking).
- **Unconfirmed:** the grace rule's presence stands, exactly as before.
  This covers a lock, a sleeping display or a sleep gap ending the stretch,
  the stretch outlasting its cap, or the daemon shutting down mid-stretch.

Details:
- **What confirms:** the user's input, even when it starts a call or video
  in the same tick (clicking Join or Play). A call or video starting on its
  own does not confirm.
- **The cap is measured by the OS idle time**, not by when the daemon first
  saw the stretch. So a restart, or a display waking on its own after hours
  idle, can't credit those hours.
- **The cap is app-aware,** like the grace windows, and scales with the
  input profile. It is set by the window the user was attending when the
  stretch began, and grows if that window shows fresh agent output during
  the stretch (waiting for an agent, then reading its output):
  - documents and browsers: 8 min;
  - terminals with fresh agent output: 8 min;
  - everything else: 4 min.

They are documented defaults, to be fitted from opt-in spot-labels in
Phase 2.

Calls and visible video still hold presence directly (ADR-0002). A call
holds it even while the display sleeps, since listening needs no screen; a
lock always means away. On macOS:
- **Calls:** a regular app (one with a Dock icon) capturing audio, even
  when its window is covered or on another Space. Background agents that
  keep the microphone open, like voice triggers, don't count. Cameras are
  not queried: CoreMediaIO loads third-party camera plugins into the
  calling process on older macOS, which would put outside code in the
  process that holds the identity key, and a camera can't be tied to an
  app (OBS keeps one on without a call). Call apps typically keep the
  microphone open while muted, so it finds the call alone. Before macOS
  14.2 only the
  default input device's state is known, and a device that also plays
  audio (a headset) is ignored so music isn't mistaken for a call.
- **Video:** a display-sleep assertion from an app or a helper inside its
  bundle. Command-line tools are left out, so `caffeinate -d` in a terminal
  doesn't count. A web page using the Screen Wake Lock API still counts as
  its browser playing video.

None of these signals needs a permission. Helper processes are mapped to
the app that owns the windows. macOS doesn't say which of an app's windows
holds the call or video, so it goes to the app's most visible window. Media
is polled every 3 s on its own goroutine, so a stalled system service only
makes media state stale; it never blocks the daemon.

## Consequences

- **Reading counts in full**, both a doc on a second monitor and Claude
  Code's output, as long as the user comes back to the keyboard or pointer
  within the cap.
- **Leaving is no longer credited** with presence when the stretch ends
  in a lock or sleep. It gets the grace rule as before, never more.
- **Overcount remains possible:** leaving without locking and returning
  within the cap counts as present. The cap bounds this error.
- **Results arrive late,** by up to the cap. Rollups are additive upserts,
  so minutes written late merge correctly.
- **Memory is bounded by the cap,** scaled for assistive profiles. Held
  ticks keep compact window facts (no titles) and one distribution each.
- **Held ticks keep their context.** Each carries the agent states from
  when it was sampled, so a permission prompt or a session ending mid-
  stretch doesn't rewrite the human timeline before it.
- **A crash loses the held stretch,** up to the cap, where before it lost
  at most the last flush interval. A clean shutdown releases it under the
  grace rule.
- **Presence intervals record `confirmed_idle` as `input`.** It is the
  tail of input presence, so the data inventory's reason values are
  unchanged. The daily health record counts seconds of call, video and
  confirmed idle (`confirmed_idle_s`), so the effect can be measured.
  Ticks released after the 04:00 rollover don't count toward the new day.
- **`burnout timeline`** shows a day minute by minute, so the result can be
  checked against a hand-written log. That is the Phase 0 exit test.

## Revisit when

- Spot-labels exist to fit the caps.
- Users report systematic overcounting from breaks taken without locking.
