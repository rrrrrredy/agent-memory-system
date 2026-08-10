# Evaluation fixtures

This directory contains synthetic, public regression inputs. It never contains
personal transcripts, legacy cards, local evidence identifiers, or reports from
a real memory repository.

The fixtures exercise pure metric behavior and the quality-profile shape.
Passing their caller-supplied thresholds does not make a report release-ready.
`Calculate` does not resolve or replay evidence. The public fixtures are
`component` inputs and do not carry the fixed policy, deterministic population,
or local runnable-oracle bindings required by `continuous_learning`.
Repeated-correction and paired-outcome values in these files are synthetic
schema examples; real continuous-learning runs must derive them from the
complete sealed paired-trial population, require supervised execution receipts
for every arm, and independently replay each
attempt with the built-in `evidence-score/v1` oracle. Token deltas use only
pairs where both attempts have an independently supplied token count; unknown
counts are not represented as measured zeroes.

- `quality-pass.json` covers all six metric categories as a component fixture.
- `empty-denominator.json` proves that an empty eligible population is
  `not_evaluable`, not a passing zero.

Real legacy corpus manifests and evaluation runs remain under the local
evidence root and must not be copied here.
