# ADR-0006: Use a local System One model (Laya) for activity classification, not for scoring

**Status:** Proposed
**Date:** 2026-10-20
**Deciders:** Project maintainers

## Context

The brief proposed **Jev** (TypeSafe AI) for predictive modelling. Jev is
a "System One" decision model. Instead of generating text, it evaluates a
text/JSON *state* against typed questions (choice, score, yes/no) and
returns answers with probabilities, in roughly 70–500 ms. It is
closed-source and cloud-only, available through TypeSafe's API and gateways
(Vercel AI Gateway, Cloudflare, Opper). Third-party write-ups say data
retention is "as long as necessary", with zero data retention reported as
enterprise/on-request only.

**Laya** (Convai Innovations) is the open-weight equivalent. It is
Apache-2.0 and Jev-compatible in style. The main English checkpoint is about
421M parameters on a ModernBERT-large backbone, and there is a smaller
multilingual variant. It runs in one forward pass (about 33–40 ms per question
on a T4 GPU, reported) and has community ONNX exports, including 4-bit
builds of around 340 MB. Laya was released in September 2026, and public
specs conflict (context length, sizes), so they must be verified in a spike.

Two separate problems could use such a model:

1. **Scoring:** turn 30 days of numeric features into a burnout risk.
2. **Classification:** turn messy activity descriptors (app, domain, window
   title, command) into role-specific categories. Rules can't do this once
   the audience includes students, researchers and PMs who work mostly in
   the browser.

## Decision

- **Scoring stays statistical, in pure Go** (ADR-0004). Neither Jev nor Laya is
  used to produce the burnout score.
- **Laya is an optional, local classifier**, step 4 of the classification
  cascade (rules → user overrides → cache → Laya → ask the user). It is
  downloaded on demand (`burnout model pull laya`, from a `models-*` release in
  this repo), never bundled, and runs in
  a **sidecar process** (`burnout-classify`) with ONNX Runtime. That keeps the
  main agent pure Go, small and crash-isolated.
- **Jev is not used.** The `Classifier` interface would technically allow a
  remote backend. The project won't ship one by default, because it would
  send activity descriptors to a third-party cloud and require each user to
  get an API key.

### Privacy of classifier inputs

- **Window titles are off by default.** They're used only when the user
  opts in to title classification. On macOS they also need the Screen
  Recording permission. Without titles, browser time is classified as
  `browser`.
- **Redaction before classification.** Titles and other free-text
  descriptors pass through a redactor before any classifier sees them. It
  strips email addresses, numbers, URLs, file paths and long opaque tokens
  (anything over 20 characters with no spaces).
- **Titles and title hashes are never persisted.** Only the resulting
  category is stored. Title hashes are low-entropy and dictionary-reversible.
  The classification cache is **in memory only**, keyed by an HMAC with the
  per-install key, and is cleared on restart.
- **Free-text check-in notes** go through the same redactor, are scored
  in memory, and are never stored.

### Sidecar requirements

The sidecar has its own budget, separate from the agent's 60 MB:

| Requirement | Value |
|-------------|-------|
| Start | on demand only; batched |
| Power | runs only on AC power |
| Volume cap | ≤ 500 items/day; the rest go to `uncategorized` |
| Memory | ≤ 1.5 GB transient while running |
| Lifetime | exits after 60 s idle |
| Network | none |
| Sandbox | Linux: Landlock + seccomp; Windows: job object + low integrity level; macOS: sandbox profile on the helper |
| Filesystem | read-only access to the model directory; no other paths |
| Model integrity | SHA-256 pinned in the signed binary (ADR-0005) |
| Protocol | versioned stdin/stdout JSON (ADR-0001 contracts) |

## Options Considered

### Option A: Jev for scoring
| Dimension | Assessment |
|-----------|------------|
| Fit | Poor: decision over text, not time-series forecasting; ignores personal baseline |
| Privacy | Fails local-first: behavioural data leaves the device |
| Cost | Per-user API key; unsuitable for a free OSS app |
| Explainability | Low |

### Option B: Laya for scoring (local)
| Dimension | Assessment |
|-----------|------------|
| Fit | Poor, same as A; numbers serialised to text lose precision and calibration |
| Privacy | OK |
| Explainability | Low |

### Option C: Laya as a local classifier (chosen)
| Dimension | Assessment |
|-----------|------------|
| Fit | Good: typed `Choice[...]` over app/domain/title is exactly its shape |
| Privacy | Better than rules + stored titles: redacted titles classified in memory; only the category is stored; no title hashes |
| Cost | ~340 MB–1.7 GB optional download; CPU inference on unique items only, batched, cached |
| Complexity | Medium: ONNX Runtime shared lib per OS + tokenizer in sidecar |

### Option D: Rules + small embedding kNN (no decision model)
| Dimension | Assessment |
|-----------|------------|
| Fit | OK for common items; weak on long tail and role nuance |
| Cost | Tiny |

D is the **fallback and the benchmark baseline**. If the spike shows Laya
isn't clearly better than D on our labelled set, we ship D.

## Trade-off Analysis

The value of a System One model here is turning unstructured context into
typed answers, and classification is where we have that problem. Scoring is
already structured numeric data, where per-user statistics are more accurate,
calibrated and explainable. Running Laya out of process avoids cgo/ONNX in
the main binary and keeps the release packages small. Users who never
enable it pay nothing.

## Consequences

- Easier: supporting non-developer roles; enabling window-title-based
  classification without storing titles.
- Harder:
  - a second binary, with per-OS ONNX Runtime packaging and CVE tracking;
  - model download and integrity checks;
  - per-OS sandbox profiles;
  - the sidecar's own resource limits, enforced and tested.
- Revisit: if Laya ships an official small/quantised CPU build or a pure-Go
  runtime becomes viable, fold it into the main agent. If Jev or a successor
  offers an on-device build, reassess.

## Action Items
1. [ ] Spike: hand-label ~500 (app, domain, title) samples across 4 roles; compare rules-only, kNN, Laya on accuracy, CPU latency, RAM.
2. [ ] Define `Classifier` interface + cascade in `internal/classify`.
3. [ ] `burnout-classify` sidecar: ONNX Runtime, tokenizer, versioned stdin/stdout JSON protocol, SHA-256-pinned model download.
4. [ ] Sidecar limits (AC-only, daily cap, memory ceiling, idle exit) and per-OS sandbox, with tests asserting no network and no unexpected file access.
5. [ ] Redactor with canary tests: emails, numbers, URLs, paths and tokens never reach the classifier or the DB.
6. [ ] In-memory HMAC-keyed classification cache; assert no title or title hash is ever written.
7. [ ] Role-profile taxonomies (developer, student, researcher, PM).
