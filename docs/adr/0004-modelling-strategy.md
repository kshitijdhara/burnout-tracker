# ADR-0004: Modelling strategy: personal-baseline heuristic first, pure-Go inference

**Status:** Proposed
**Date:** 2026-10-20
**Deciders:** Project maintainers

## Context

The brief proposed Jev (TypeSafe AI's System One decision model) for
predictive modelling. ADR-0006 covers why Jev/Laya are used for
classification, not scoring. Scoring has problems of its own:

1. **No labels on day one.** Burnout isn't observable from device usage, so
   we need ground truth (self-report) before any supervised model is
   meaningful.
2. **Runtime.** The model has to run on-device inside a Go agent shipped via
   GitHub Releases.
3. **Relative scores can be fooled.** A baseline learned during a crunch
   treats the crunch as normal. Features that are usually zero break naive
   z-scores. Days the device wasn't used look like rest.

## Decision

### Phase 1: transparent strain score (cold start)

1. **Coverage first.** Each logical day records *coverage*: the share of
   logged-in time the agent observed. Days below the threshold (default
   60%), or days the device wasn't used, are **missing, not rest**. They
   aren't scored and don't enter baselines. Multi-device users are told what
   coverage means.
2. **Baselines per day type.** Each feature gets a robust median and MAD
   over a trailing 28-day window, computed **separately for weekdays and
   weekends**. Scores start after a 14-day calibration period.
3. **Robust deviation with a floor:** `z_f = (x_f − median_f) / max(1.4826·MAD_f, ε_f)`.
   Each feature has its own `ε_f`. The z is signed so "worse" is positive
   and clipped to ±4. Zero-inflated features (weekend minutes, failure
   loops) are modelled as rates or counts per active hour, not raw minutes.
4. **Absolute guardrails beside the relative score.** These are
   evidence-based limits that apply whatever the baseline says. For example:
   - ≥ 55 h/week of attended work (WHO/ILO long-working-hours evidence);
   - < 7 h overnight gap between last and first activity.

   Guardrail breaches add to the composite directly. They also **seed the
   long-term anchor**, so a baseline learned during a crunch can't hide
   chronic overload.
5. **Long-term anchor.** The anchor is the best 28-day window in the last 6
   months, where "best" means the lowest composite with no guardrail
   breaches. Until one exists, the guardrails define it. Scores are computed
   against a blend of the rolling baseline and the anchor.
6. **Grouped composite.** Features belong to groups: load, fragmentation,
   recovery, friction, agent work. Each group's contribution is capped, so
   correlated features (hours, after-hours, last activity) can't triple-count
   one signal. Then `composite = Σ_g min(cap_g, Σ_{f∈g} w_f · EWMA_7d(z_f))`.
7. **0–100 mapping:** `strain = 100 / (1 + exp(−k·(composite − c)))`, with
   `k` and `c` documented and versioned alongside the feature registry
   (ADR-0001).
8. **Annotations.** Users can tag periods:
   - *vacation* and *sick*: excluded from baselines;
   - *on-call*, *exam week*, *release*: planned crunch. These are still
     scored, but the explanation says so, and the following week's recovery
     is emphasised.
9. **Explainability:** the top 3 contributing features are always shown, each
   with its group and its deviation from the user's own baseline.

### Labels

A weekly Copenhagen Burnout Inventory check-in, plus an optional daily
micro check-in (energy, how drained) for denser labels. Only validated
translations of the CBI are used.

### Phase 2: personal model and forecast

- **Model:** a per-user Bayesian ordinal regression of CBI band on lagged
  weekly features, with strong priors.
- **Forecast:** a level-plus-slope trend on daily strain, used to estimate
  time to the user's threshold. It's **suppressed whenever the slope's
  confidence interval includes 0**, and always shown as a range, never a
  single date.

### Validation

- Repeated-measures correlation (**rmcorr**) between strain and CBI across
  alpha users. The alpha runs **≥ 8 weeks**, and the daily micro check-in
  provides extra points.
- The analysis plan, target effect size and exclusion rules are
  **pre-registered in `research/`** before the data comes in.

### Phase 4: population priors and hazard model

These need donated data. The research export is gated on:
- coarsened or bucketed features, with no exact timestamps;
- a k-anonymity threshold;
- the user previewing the exact payload;
- explicit consent text;
- ethics/IRB review.

Burnout-related scores are health data (GDPR Art. 9 special category).
Maintainers who publish a dataset become its data controllers, so that
needs a written policy first.

### Stack

Research and training in Python (NumPy / PyMC / scikit-learn) on synthetic
or donated data. Coefficients ship as JSON. **Inference runs in pure Go**
(gonum). `leaves` (pure-Go GBDT inference) is the escape hatch if tree
models win.

## Options Considered

### Option A: Pure-Go inference, Python research (chosen)
| Dimension | Assessment |
|-----------|------------|
| Complexity | Low at runtime |
| Packaging | No extra runtimes in the main binary |
| Model families | Linear, Bayesian-linear, GBDT via `leaves` |

### Option B: Embed ONNX Runtime (`onnxruntime_go`)
| Dimension | Assessment |
|-----------|------------|
| Complexity | Medium (cgo + shared lib per platform) |
| Packaging | +15–30 MB; per-OS libs; signing headaches |
| Model families | Anything exportable to ONNX |

### Option C: Bundle a Python runtime
Rejected: hundreds of MB, fragile installs, and a much larger attack surface.

### Option D: Supervised model from day one
Rejected: no labels, so any "prediction" would be made-up thresholds
presented as ML.

### Option E: Ask a decision model (Jev/Laya) for the score
Rejected: see ADR-0006. These models decide over text, not time series.

### Option F: Purely relative scoring (the v0.1 design)
Rejected: misses chronic overload when calibration happens during a crunch,
divides by zero on zero-inflated features, mixes weekdays with weekends, and
counts missing days as rest.

## Trade-off Analysis

With small per-user data (dozens of weekly points), well-regularised linear
or Bayesian models are about as good as anything heavier, and they are
explainable. Explainability matters more here than a few points of AUC:
users need to see *why* the score moved.

Guardrails, coverage and per-group caps add parameters we have to justify.
But each one fixes a failure mode that would otherwise make the headline
number wrong for exactly the users who need it most.

## Consequences

- Easier: shipping, explainability, deterministic tests, honest handling
  of missing data and planned crunch.
- Harder: guardrail thresholds and group caps need literature citations and
  sensitivity analysis; we can't use deep sequence models without revisiting
  B.
- The forecast ("weeks until high-strain zone") is a trend extrapolation, not
  a clinical prediction, and the UI copy must say so.
- Revisit: B if a heavier model clearly beats the baseline in research;
  guardrail values after the alpha.

## Action Items
1. [ ] `internal/score`: coverage gating, day-type baselines, MAD floors, guardrails, anchor, grouped composite with caps, logistic mapping, top-3 drivers.
2. [ ] Feature registry entries with group, direction, `ε_f` and guardrail references (ADR-0001).
3. [ ] Annotations (vacation, sick, on-call, exam week, release) in storage, UI and scoring.
4. [ ] Check-in UI + storage; CBI scoring; daily micro check-in; validated translations only.
5. [ ] Forecast with CI-based suppression.
6. [ ] `research/`: synthetic data generator, sensitivity analysis for guardrails and caps, pre-registered rmcorr analysis plan.
7. [ ] Research-export gate (Phase 4): coarsening, k-anonymity, preview, consent, ethics review, controller policy.
8. [ ] Legal/wording review: wellness positioning, "not a medical device".
