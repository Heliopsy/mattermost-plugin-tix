# AGENTS.md

Mattermost plugin for [tix](https://github.com/heliopsy/tix): a Go server plugin (`server/`) and a React/TypeScript
webapp plugin (`webapp/`). AGPL-3.0; every new source file starts with `// SPDX-License-Identifier: AGPL-3.0-only`.

**Before editing `server/`, read `server/AGENTS.md`. Before editing `webapp/`, read `webapp/AGENTS.md`.**
The tix HTTP API is described by `api/openapi.yaml` and `docs/api.md` in the tix repository; never guess a field.

## Architecture

- `server/tix` is the tix API client. It marshals and nothing else; tix decides the rules.
- `server/store` owns every KV key: account links (token encrypted with AES-GCM), channel subscriptions, the
  task-ref to card-post index, and `Get`/`Set` for small per-feature records (`x_<feature>_<key>`).
- `server/app` is what features share: `TixFor(userID)` (a client acting as that person, or `ErrNotLinked`),
  cached projects, workflows and actors, `MentionFor`, `Ephemeral`, `DM`, `PluginURL`, `ErrText`.
- `server/command` owns `/tix`: features add `command.Sub`s; the registry builds autocomplete and help.
- `server/api` owns the router: `Public` (`/hooks/...`, no Mattermost session; `/public/` is Mattermost's static path; each route authenticates itself)
  and `User` (`/api/v1/...`, `Mattermost-User-ID` set by the server).
- Features are packages with `New`, `Commands()` and `Routes(*api.Handler)`, wired in `server/plugin.go`:
  `link` (connect, disconnect, me), `tasks` (working tasks by command), `cards` (task cards and actions),
  `notify` (tix webhooks to channels and DMs), `digest` (the daily digest and reminders job), `webapi` (the JSON API
  behind the webapp). A feature touches only its own package; a change to a shared
  package (`app`, `store`, `tix`, `command`, `api`) is small, additive and called out in the commit.
- Every tix call acts as the Mattermost user who caused it, through their own token. The admin token is used only
  to issue those tokens and to manage the webhook, never to act for a person.

## Commands

```bash
make apply                                     # generate manifest.go / manifest.ts (needed on a fresh checkout)
go build ./... && go vet ./... && go test ./server/...
cd webapp && npm ci && npm run check-types && npm run lint && npx jest
make dist                                      # the plugin bundle in dist/
make e2e                                       # real Mattermost + tix in podman (see docs/development.md)
```

Commits are Conventional Commits, concise, no Co-Authored-By trailers. OpenSpec specs in `openspec/specs/`
describe behaviour; update the spec with the code. User docs live in `docs/`; keep README.md short and link there.
Tool versions are pinned in `.tool-versions`.

## Rules that apply everywhere

- The server only authenticates plugin requests. Every route and command that touches data checks the user's permission on that object, on the server; hiding UI is not a check.
- Anything that changes state uses POST, PUT, PATCH or DELETE. The server's CSRF check doesn't cover GET.
- Never send internal errors to clients or write secrets, tokens or message content to logs.
- Don't add dependencies the host, Compass or the standard library already provide. Fix CodeQL alerts instead of adding exclusions to `.github/codeql/codeql-config.yml`; the only one is `go/log-injection`, which the server excludes too.
- In GitHub workflows, pin third-party actions to a commit SHA with the version in a comment, and start from `permissions: {}`.

## Pull requests and commits

- Commit subject: one succinct line. Don't add a `Co-Authored-By` line for the agent.
- In the PR description, call out any change to permissions, authentication, secrets or `min_server_version`.
