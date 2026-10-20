# burnout-tracker

A local-first desktop agent that helps you spot early warning signs in your
work patterns. It measures attention (multi-monitor aware), terminal
activity and AI-agent sessions (Claude Code reading vs. waiting time), then
compares them against your own baseline and absolute limits.

> **Not a medical device.** It reports behavioural signals for
> self-reflection, not diagnoses.

> **For self-use only.** Don't install it to monitor anyone else. It always
> shows a tray icon while collecting, can't be installed silently, has no
> remote access, and only starts after the person using the computer
> completes setup themselves.

## Privacy in one paragraph

Everything stays on your device. There is no server and no telemetry. The
only outbound connections are three opt-in ones: calendar feed, update
check, model download. Window titles, command arguments and prompt text are
never stored. [Privacy & Security](docs/privacy-and-security.md) lists
every field that is collected.

## Status

Design phase (v0.2). Start here:

- [System design](docs/system-design.md)
- [Privacy & Security](docs/privacy-and-security.md): the threat model and
  data inventory. This is the source of truth for what is collected.
- ADRs:
  - [0001 local-first Go agent](docs/adr/0001-local-first-go-agent.md)
  - [0002 attention model](docs/adr/0002-attention-model-over-active-window.md)
  - [0003 hooks instrumentation](docs/adr/0003-instrumentation-via-hooks.md)
  - [0004 modelling strategy](docs/adr/0004-modelling-strategy.md)
  - [0005 packaging via GitHub Releases](docs/adr/0005-packaging-and-distribution.md)
  - [0006 Laya/Jev for classification](docs/adr/0006-decision-model-for-classification.md)
  - [0007 data protection at rest](docs/adr/0007-data-protection-at-rest.md)
- [Design review 2026-10-20](docs/reviews/2026-10-20-design-review.md):
  findings and how v0.2 resolves them.
