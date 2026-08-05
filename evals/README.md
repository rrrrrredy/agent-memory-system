# Evaluation fixtures

This directory contains synthetic, public regression inputs. It never contains
personal transcripts, legacy cards, local evidence identifiers, or reports from
a real memory repository.

The fixtures exercise pure metric behavior and the quality-profile shape.
Passing their caller-supplied thresholds does not make a report release-ready.
`Calculate` does not resolve or replay evidence, and even a fully replayed
`continuous_learning` run remains measurement-only until fixed policy,
deterministic population reconstruction, and independently runnable oracles are
implemented. Repeated-correction and paired-outcome values in these public
fixtures are synthetic schema examples; real runs must derive them from local
task-attempt receipts.

- `quality-pass.json` covers all six continuous-learning metric categories.
- `empty-denominator.json` proves that an empty eligible population is
  `not_evaluable`, not a passing zero.

Real legacy corpus manifests and evaluation runs remain under the local
evidence root and must not be copied here.
