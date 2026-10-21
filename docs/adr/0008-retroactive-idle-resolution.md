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
an idle stretch are also marked idle, carry the attention distribution the
user would have if present, and carry a cap. A pure `attention.Resolver`
holds idle ticks until the stretch ends, then releases them in order:

- **Confirmed (reading):** the stretch ends with the user's own input, it
  was contiguous (no sleep gap), and it ended within its cap. Every tick
  counts as fully present, with reason `reading`.
- **Unconfirmed:** a lock, display sleep or sleep gap ends the stretch, or
  it outlasts its cap, or the daemon shuts down mid-stretch. The grace
  rule's presence stands, exactly as before.

A call or video starting on its own does not confirm a stretch: it isn't
the user's input.

Caps are app-aware like the grace windows, and scale with the input
profile:
- **Documents and browsers:** 8 min.
- **Terminals with fresh agent output:** 8 min.
- **Everything else:** 4 min.

They are documented defaults, to be fitted from opt-in spot-labels in
Phase 2.

Calls and visible video still hold presence directly (ADR-0002). On macOS:
- **Calls:** a regular app (one with a Dock icon) capturing audio, even
  when its window is covered or on another Space, or any camera in use.
  Background agents that keep the microphone open, like voice triggers,
  don't count.
- **Video:** an app holding a display-sleep assertion.

None of these signals needs a permission. Helper processes are mapped to
the app that owns the windows.

## Consequences

- **Reading counts in full**, both a doc on a second monitor and Claude
  Code's output, as long as the user comes back to the keyboard or pointer
  within the cap.
- **Leaving is no longer credited** with presence when the stretch ends
  in a lock or sleep. It gets the grace rule as before, never more.
- **Overcount remains possible:** leaving without locking and returning
  within the cap counts as present. The cap bounds this error.
- **Results arrive late,** by up to the cap. Rollups are additive upserts,
  so minutes written late merge correctly. Memory is bounded by the cap,
  scaled for assistive profiles.
- **Presence intervals record `reading` as `input`.** It is the tail of
  input presence, so the data inventory's reason values are unchanged.
  The daily health record counts seconds of call, video and confirmed
  reading, so the effect can be measured.
- **`burnout timeline`** shows a day minute by minute, so the result can be
  checked against a hand-written log. That is the Phase 0 exit test.

## Revisit when

- Spot-labels exist to fit the caps.
- Users report systematic overcounting from breaks taken without locking.
