# ADR-0010: English as the repository language

## Status

Accepted

## Date

2026-09-12

## Context

The project is designed and discussed in Spanish, because that is the language its author works in.
It is also intended to be open source, which means contributors, issues, and pull requests from
people who do not read Spanish.

Research notes and design documents already exist in Spanish, outside this repository.

## Decision

Everything inside this repository is in English. Code, identifiers, comments, commit messages, issue
templates, documentation, and these decision records.

Research and design material outside the repository stays in Spanish. That material is the author's
working context, not a contributor artifact. Where a decision record depends on a finding recorded in
Spanish, the record restates the finding in English rather than linking a reader to a document they
cannot read.

## Alternatives Considered

### Spanish inside the repository as well

- Pros: no translation work, one language for the author.
- Cons: excludes most potential contributors from a project whose value depends on adoption.
- Rejected.

### Both languages, side by side

- Pros: nobody is excluded.
- Cons: two documents drift apart, and the reader can never tell which one is authoritative. The cost
  is paid on every change, forever.
- Rejected.

## Consequences

- The protocol findings, which were produced in Spanish, are restated in English in
  [`docs/protocol.md`](../protocol.md) so this repository is self contained.
- Contributors need no Spanish to work on the project.
- The author pays a small translation cost whenever a design decision crosses from the research
  material into the repository. That cost is deliberate and is the point of the boundary.
