# Development

## Tools

The versions are pinned in `.tool-versions` (asdf or mise): Go, Node, golangci-lint and Python (for the
end-to-end suite). Containers run with rootless podman.

```bash
asdf install            # or: mise install
make apply              # generate server/manifest.go and webapp/src/manifest.ts from plugin.json
make dist               # the bundle in dist/cc.heliopsy.tix-<version>.tar.gz
```

## Checks

```bash
go build ./... && go vet ./... && bin/golangci-lint run ./...   # `make install-go-tools` installs the linter
SKIP_DOCKER_TESTS=1 go test ./server/...
cd webapp && npm ci && npm run check-types && npm run lint && npx jest
make e2e                                                         # see below
```

## End-to-end tests

`make e2e` runs the plugin in a real Mattermost against a real tix and checks what people see:

```bash
make e2e                                   # build, start, seed, run every scenario, tear down
E2E_FLAGS="--only card --only sync" make e2e   # setup always runs; then only matching scenarios
E2E_KEEP=1 make e2e                        # keep the images it pulled, for faster reruns
```

It needs Go, Node, Python 3 (standard library only) and rootless podman. The suite (`e2e/run.py`) builds the
bundle for this machine, starts `docker.io/mattermost/mattermost-preview` as `tix-e2e-mm` and
`ghcr.io/heliopsy/tix` as `tix-e2e-tix` in the same network namespace (tix only delivers plain-http webhooks to
loopback), publishes both on random `127.0.0.1` ports, seeds them (the tix demo data, a `review` workflow with a
transition that requires a comment, people in both, a plugin admin token), and prints one `PASS` or `FAIL` line
per scenario and a summary. It always removes its containers and volume, also on failure or Ctrl-C, and
deletes the images it pulled unless `E2E_KEEP=1`. `E2E_TIX_IMAGE` and `E2E_MM_IMAGE` pick other versions. The
scenarios are in `e2e/scenarios.py`; the `e2e` workflow runs them on pull requests.

## Strings and languages

Every user-facing text has a message id. The server's catalogs are `assets/i18n/en.json` and `bg.json`
(embedded into the binary by `assets/assets.go`, package `server/i18n`); the webapp's are `webapp/i18n/en.json`
and `bg.json` (react-intl).

To add a server string, pick an id `<package>.<area>.<name>`, add it to both server catalogs (keep them sorted)
and render it with the reader's localizer:

```go
c.L().T("cards.sync.is_on")                     // a command reply, in the caller's language
f.a.Locale(userID).T(...)                        // an ephemeral or DM, in that person's language
f.a.ServerLocale().T(...)                        // a channel post, in the server's default language
l.N("digest.more", n)                            // a count: {"one": "...", "other": "..."}, {count} is n
i18n.NewError("tasks.err.no_title")              // an error, rendered by app.ErrText(l, err)
```

Placeholders are `{name}`, filled from name, value pairs; there is no template language. An id used through a
table or variable is wrapped in `i18n.M("...")` so the catalog test finds it. In the webapp use
`<FormattedMessage id=... defaultMessage=.../>` (or `useIntl().formatMessage` for string props) and add the
text to both webapp catalogs. Command names, option keywords and filter syntax stay English; workflow state
labels come from tix untranslated.

`go test ./server/i18n` fails when code uses an id `en.json` lacks, when `bg.json` lacks an id or its placeholders
or plural forms differ, and warns about ids no code uses; `webapp/src/i18n.test.ts` does the same for the
webapp and checks each `defaultMessage` against `en.json`.

To add a language, copy both `en.json` files to `<code>.json` (the Mattermost locale, such as `de`), translate
them, and add the code to `pickMessages` in `webapp/src/i18n.ts`. The server picks it up from the file name; if
the language has plural forms other than one and other, add its CLDR rule to `pluralOne` in
`server/i18n/i18n.go` (and use a fitting form set in the catalog).

## Trying it on a local Mattermost

With local mode on (`ServiceSettings.EnableLocalMode`), `make deploy` installs the bundle through the local
socket; `MM_LOCALSOCKETPATH` picks another socket. Against a remote server, set `MM_SERVICESETTINGS_SITEURL` and
`MM_ADMIN_TOKEN` (or `MM_ADMIN_USERNAME` and `MM_ADMIN_PASSWORD`). `make watch` rebuilds and redeploys the webapp
on change.

## Layout

| Path | What lives there |
| --- | --- |
| `server/tix` | The tix API client. It marshals and nothing else; tix decides the rules. |
| `server/store` | Every KV record: account links (tokens encrypted), subscriptions, the card index, feature records. |
| `server/app` | What features share: a tix client per person, cached projects, workflows and actors, DMs, each person's language and time zone. |
| `server/i18n`, `assets/i18n` | The translator and the server's message catalogs. |
| `server/render` | The formatting rules every feature shares: dates, due dates, comments, people, card fields, digest sections. |
| `server/command`, `server/api` | The `/tix` registry and the HTTP router (`/api/v1` for the webapp, `/hooks` for tix). |
| `server/link` | Connecting accounts. |
| `server/tasks` | Task commands, channel defaults, the create reaction, search. |
| `server/cards` | Task cards, their buttons and dialogs, thread sync. |
| `server/notify` | The webhook receiver, channel feeds and DMs. |
| `server/digest` | The daily digest and reminders job. |
| `server/webapi`, `webapp/` | The JSON API and the React sidebar, task view and create dialog. |
| `e2e/` | The end-to-end suite. |
| `openspec/specs/` | The behaviour specs; change them with the code. |

## Releasing

Bump `version` in `plugin.json`, add the release to `CHANGELOG.md`, then:

```bash
make dist
git tag -a vX.Y.Z -m vX.Y.Z && git push origin vX.Y.Z
gh release create vX.Y.Z dist/cc.heliopsy.tix-X.Y.Z.tar.gz --title vX.Y.Z --notes-file <notes>
```
