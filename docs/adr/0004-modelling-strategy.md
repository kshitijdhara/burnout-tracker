# ADR-0004: Modelling strategy: personal-baseline heuristic first, pure-Go inference

**Status:** Proposed
**Date:** 2026-10-20
**Deciders:** Project maintainers

## Context

The brief proposed Jev (TypeSafe AI's System One decision model) for
predictive modelling. ADR-0006 covers why Jev/Laya are used for
classification, not scoring. Scoring has two problems of its own:

1. **No labels on day one.** Burnout isn't observable from device usage;
   we need ground truth (self-report) before any supervised model is meaningful.
2. **Runtime.** The model has to run on-device inside a Go agent shipped via
   package managers.

## Decision

- **Phase 1:** a transparent strain score made of robust z-scores against the
  user's own baseline (28-day rolling + long-term anchor), smoothed with an
  EWMA and weighted into a composite. It needs a 14-day calibration period.
- **Labels:** weekly Copenhagen Burnout Inventory check-in + optional daily
  energy slider.
- **Phase 2:** a per-user Bayesian/ordinal regression on lagged weekly
  features, and a trend forecast for time-to-threshold with uncertainty bands.
- **Phase 4:** population priors and a hazard model trained offline on
  opt-in donated aggregates.
- **Stack:** research and training in Python (NumPy/PyMC/scikit-learn).
  Ship coefficients as JSON; **inference in pure Go** (gonum). `leaves`
  (pure-Go GBDT inference) is the escape hatch if tree models win.

## Options Considered

### Option A: Pure-Go inference, Python research (chosen)
| Dimension | Assessment |
|-----------|------------|
| Complexity | Low at runtime |
| Packaging | Single binary, no extra runtimes |
| Model families | Linear, Bayesian-linear, GBDT via `leaves` |

### Option B: Embed ONNX Runtime (`onnxruntime_go`)
| Dimension | Assessment |
|-----------|------------|
| Complexity | Medium (cgo + shared lib per platform) |
| Packaging | +15–30 MB; per-OS libs; signing headaches |
| Model families | Anything exportable to ONNX |

### Option C: Bundle Python/JAX
Rejected: hundreds of MB, fragile installs, and much larger attack surface.

### Option E: Ask a decision model (Jev/Laya) for the score
Rejected: see ADR-0006. These models decide over text, not time series.

### Option D: Supervised model from day one
Rejected: no labels, so any "prediction" would be made-up thresholds
presented as ML.

## Trade-off Analysis

With small per-user data (dozens of weekly points), well-regularised linear
or Bayesian models are about as good as anything heavier, and they are
explainable. Explainability matters more here than a few points of AUC:
users need to see *why* the score moved. A heuristic baseline is honest about
what we know and it collects the labels needed to improve it.

## Consequences

- Easier: shipping, explainability, deterministic tests.
- Harder: we can't use deep sequence models without revisiting B.
- The forecast ("weeks until high-strain zone") is a trend extrapolation, not
  a clinical prediction, and the UI copy must say so.
- Revisit: B if a heavier model clearly beats the baseline in research.

## Action Items
1. [ ] `internal/score`: baseline (median/MAD), EWMA, composite, top-drivers.
2. [ ] Check-in UI + storage; CBI scoring.
3. [ ] `research/`: synthetic data generator + notebook to fit weights; JSON export format.
4. [ ] Legal/wording review: "not a medical device" positioning.
