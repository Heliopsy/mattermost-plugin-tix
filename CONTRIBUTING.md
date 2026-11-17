# Contributing

Issues and pull requests are welcome.

- Read [AGENTS.md](AGENTS.md) for the architecture rules and [docs/development.md](docs/development.md) for the
  tools and checks. Tool versions are in `.tool-versions`.
- Behaviour changes update the spec in `openspec/specs/` together with the code; `openspec validate --strict`
  passes.
- Before opening a pull request: `go vet ./...`, `bin/golangci-lint run ./...`, `go test ./server/...`, the webapp
  checks, and `make e2e` when the change touches what people see.
- Commits follow [Conventional Commits](https://www.conventionalcommits.org/) (`feat:`, `fix:`, `docs:` ...).
- New source files start with `// SPDX-License-Identifier: AGPL-3.0-only`. Contributions are accepted under the
  project's license, AGPL-3.0.
