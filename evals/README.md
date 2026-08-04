# Evaluation fixtures

This directory contains synthetic, public regression inputs. It never contains
personal transcripts, legacy cards, local evidence identifiers, or reports from
a real memory repository.

The fixtures exercise pure metric behavior. Passing their thresholds does not
make a report release-ready because `Calculate` does not resolve evidence. A
release gate requires `agentmem eval run` against a local evidence store.

- `quality-pass.json` covers all six continuous-learning metric categories.
- `empty-denominator.json` proves that an empty eligible population is
  `not_evaluable`, not a passing zero.

Real legacy corpus manifests and evaluation runs remain under the local
evidence root and must not be copied here.
