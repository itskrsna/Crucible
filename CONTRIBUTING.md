# Contributing

```sh
go test ./...                                   # unit tests, no network or Docker needed
CRUCIBLE_DOCKER_TEST=1 go test ./internal/sandbox  # real Docker sandbox test
```

- The standard library only, unless a dependency clearly earns its place.
- Each pipeline stage is a package under `internal/` with fakes for its dependencies (`llm.Chatter`,
  `sandbox.Executor`), so stages are testable without a model or Docker.
- Verdict logic lives in `internal/compare`. Changes to tolerances need a test and a note on why.
- Please open an issue before large changes.

## Reporting a wrong record

Open an issue with the arXiv id and what is wrong. Records with a mistake will be corrected or withdrawn.
