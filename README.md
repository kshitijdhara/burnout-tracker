# burnout-tracker

A local-first desktop agent that learns how you work and flags burnout risk
early. It measures attention (multi-monitor aware), terminal activity and
AI-agent sessions (Claude Code reading vs. waiting time), and scores them
against your own baseline.

> Not a medical device. It reports behavioural risk signals, not diagnoses.

## Status

Design phase. Start here:

- [System design](docs/system-design.md)
- ADRs: [0001 local-first Go agent](docs/adr/0001-local-first-go-agent.md) ·
  [0002 attention model](docs/adr/0002-attention-model-over-active-window.md) ·
  [0003 hooks instrumentation](docs/adr/0003-instrumentation-via-hooks.md) ·
  [0004 modelling strategy](docs/adr/0004-modelling-strategy.md) ·
  [0005 packaging via GitHub Releases](docs/adr/0005-packaging-and-distribution.md) ·
  [0006 Laya/Jev for classification](docs/adr/0006-decision-model-for-classification.md)
