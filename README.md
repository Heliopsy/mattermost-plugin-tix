# Plugin Starter Template

[![Build Status](https://github.com/heliopsy/mattermost-plugin-tix/actions/workflows/ci.yml/badge.svg)](https://github.com/heliopsy/mattermost-plugin-tix/actions/workflows/ci.yml)
[![E2E Status](https://github.com/heliopsy/mattermost-plugin-tix/actions/workflows/e2e.yml/badge.svg)](https://github.com/heliopsy/mattermost-plugin-tix/actions/workflows/e2e.yml)

This plugin serves as a starting point for writing a Mattermost plugin. Feel free to base your own plugin off this repository.

To learn more about plugins, see [our plugin documentation](https://developers.mattermost.com/extend/plugins/).

This template targets Mattermost Server 11.7 or later (`min_server_version` in `plugin.json`), the oldest supported ESR. The webapp builds against the React 18 that 11.7 ships and runs unchanged on React 19 in 12.0. It requires the Go version in `go.mod` and the Node version in `.nvmrc`. You can download and install nvm to manage your node versions by following the instructions [here](https://github.com/nvm-sh/nvm). Once you've setup the project simply run `nvm i` within the root folder to use the suggested version of node.

## In the app

- **tix sidebar.** The tix button in the channel header (or the App Bar) opens a sidebar. **Mine** lists your open tasks grouped by status; **Project** lets you pick a project and search it. Select a task to see its status, assignee, priority, tags, description and comments, move it along its workflow (a move that needs a comment asks for one), assign it to yourself, unassign it, change its priority or comment. **New task** opens the create dialog.
- **Create from a message.** In a message's **...** menu, **Create tix task** opens a dialog with the message as the title and description and a link back to it. Pick the project, assignee (you by default) and priority. After creating, **Post link in thread** replies with the task reference, which turns into a task card.
- **Create from anywhere.** The main menu has **Create tix task** too; its link goes into the current channel.
- **Not connected yet?** The sidebar and dialog show a **Connect** button when the admin token is configured (it matches your verified email to your tix account), or tell you to run `/tix connect`.

Everything happens as you in tix, through your own token; tix decides what you can see and change.

## Getting Started
Use GitHub's template feature to make a copy of this repository by clicking the "Use this template" button.

Alternatively shallow clone the repository matching your plugin name:
```
git clone --depth 1 https://github.com/heliopsy/mattermost-plugin-tix com.example.my-plugin
```

Note that this project uses [Go modules](https://github.com/golang/go/wiki/Modules). Be sure to locate the project outside of `$GOPATH`.

Edit the following files:
1. `plugin.json` with your `id`, `name`, and `description`:
```json
{
    "id": "com.example.my-plugin",
    "name": "My Plugin",
    "description": "A plugin to enhance Mattermost."
}
```

2. Replace the Go module path (`go.mod`, imports, mocks, `Makefile`, `.golangci.yml`) with yours, following the `<hosting-site>/<repository>/<module>` convention. On Linux, drop the `''` after `-i`:
```bash
git grep -l github.com/heliopsy/mattermost-plugin-tix -- go.mod '*.go' Makefile .golangci.yml \
  | xargs sed -i '' 's|github.com/heliopsy/mattermost-plugin-tix|github.com/example/my-plugin|g'
```

3. Build your plugin:
```
make
```

This will produce a single plugin file (with support for multiple architectures) for upload to your Mattermost server:

```
dist/com.example.my-plugin.tar.gz
```

## Commands

Task commands act as you, through the tix token you linked with `/tix connect`. Replies are only visible to you
unless you ask for a channel post. References autocomplete from your open tasks; `/tix move` suggests the states
the task can go to.

| Command | What it does |
| --- | --- |
| `/tix new [project] <title> [--assign @user\|me] [--priority high] [--post] [--raw]` | Create a task. The title takes tix quick-add tokens (`!high #tag @user due:fri +project`); `--post` announces it in the channel, `--raw` keeps the title as typed. |
| `/tix show <ref>` | Status, assignee, priority, due date, tags, description excerpt and the last three comments. |
| `/tix list [mine\|all\|<project>] [filter]` | Your open tasks by default, grouped by status. A project key lists that project; `all` includes finished tasks; filter terms follow tix's filter language, e.g. `status:todo -tag:ops @bob priority:high due:week title~api`. |
| `/tix start <ref> [comment]` | Move to the workflow's in-progress state. |
| `/tix done <ref> [comment]` | Move to the workflow's done state. |
| `/tix close <ref> [comment]` | Close without doing it: the cancelled state, or done when the workflow has none. |
| `/tix reopen <ref> [comment]` | Move a finished task back to an open state. |
| `/tix move <ref> <state> [comment]` | Move to a state by key or label. |
| `/tix comment <ref> <text>` | Comment on a task (Markdown, line breaks kept). |
| `/tix assign <ref> <@user\|me\|none>` | Assign to a linked Mattermost user, yourself or a tix handle; `none` unassigns. |

Moves follow the task's workflow. When there is no direct transition, the reply lists the states reachable from
the current one and the shortest route; when a transition requires a comment, add it after the reference.

## Try the example

Once the plugin is installed and enabled (System Console > Plugins > Plugin Management, or `make deploy`):

1. In any channel, run `/channelstatus at-risk`. An **At risk** tag appears in the channel header for everyone in the channel, without a refresh. `/channelstatus` on its own shows the current status, and `/channelstatus clear` removes it.
2. Click the tag, or the flag icon in the App Bar on the right edge of the window, to open the **Channel status** sidebar. Pick a status and click **Save**, or click **Clear status**. Who can change a status follows the permission for editing the channel header (System Console > User Management > Permissions); everyone else sees it read-only.
3. Open System Console > Plugins > Plugin Starter Template, click **Regenerate** next to **Webhook Secret**, then **Save**. The webhook rejects every call until a secret exists. Then:
   ```bash
   curl -i -X POST -H "X-Webhook-Secret: <secret>" <SiteURL>/plugins/com.mattermost.plugin-starter-template/webhook
   ```
   returns `204`. Without the header, or with a wrong secret, it returns `401`.

## Notifications and channel feeds

tix tells the plugin what happens through a signed webhook. The plugin turns each event into posts in subscribed
channels, DMs to the people concerned and redrawn task cards.

### Setup

1. In tix, make a token for the plugin with `token:admin`, `user:admin` and `tenant:admin` (to find people by email and read
   their role), `webhook:admin`, and every scope it hands out to people (`task:read`, `task:write`,
   `task:transition`, `task:claim`, `project:read`, `workflow:read`, `comment:write`, `artifact:write`,
   `event:subscribe`, `audit:read`): tix only lets a token grant scopes it holds. An admin-role token (`*`) works. Paste it into System Console > Plugins > tix > **Admin token**. `webhook:admin` registers
   the webhook; the read scopes let the plugin show task titles, comment text and workflow state labels
   ("To do → Doing"). Without the read scopes it falls back to the token of the person who caused the event,
   when they have connected, and otherwise to raw state keys.
2. Click **Regenerate** next to **Webhook secret** and save. Until a secret exists the receiver rejects everything.
3. Run `/tix webhook setup` as a system admin. It registers (or updates) a tenant webhook in tix pointing at
   `<SiteURL>/plugins/cc.heliopsy.tix/hooks/webhook`, signed with that secret, for task changes and
   `comment.added`. tix only delivers to `https` URLs on public addresses (an operator can relax this with
   `webhooks.allow_private_targets`). `/tix webhook status` shows the endpoint and its last deliveries;
   `/tix webhook remove` deletes it. After regenerating the secret, run `/tix webhook setup` again.

The receiver checks `X-Tix-Signature` (`sha256=` and the hex HMAC-SHA256 of `X-Tix-Timestamp`, a dot and the raw
body), compared in constant time, refuses a timestamp more than five minutes off, and ignores an event it has
already handled, so tix's retries never post twice.

### Channel feeds

| Command | What it does |
| --- | --- |
| `/tix subscribe <project> [events...]` | Post a project's activity in this channel |
| `/tix subscribe all [events...]` | Every project (system admins only) |
| `/tix unsubscribe <project\|all>` | Stop a feed |
| `/tix subscriptions [all]` | This channel's feeds; `all` lists every channel's (system admins) |

Events: `created`, `transitioned` (with the from and to states), `assigned`, `commented`, `done` (moved into a
done or cancelled state; `closed` works too) and `deleted`. Without events a feed gets `created`,
`transitioned` and `commented`. Posts come from the tix bot, one line each, for example
`**alice** moved [infra-42 Fix backups](...) **To do → Doing**`. Feed posts name people without @-mentioning
them, so nobody is pinged by a feed.

Only people who can manage the channel's settings (channel admins, or anyone with the manage public/private
channel properties permission) can add or remove its feeds, and they must be connected to tix and able to see
the project. **A feed publishes.** Whoever subscribes a channel decides that everyone who can read the channel
sees that project's task titles and comment text, including people who cannot open the project in tix. The
subscribe reply says so.

### Personal notifications

Connected people get a DM from the bot when:

- a task is assigned to them;
- someone comments on a task they are assigned to or created;
- a task they created is moved to a done or cancelled state;
- they are @mentioned (by their tix handle) in a comment or a new task's description, and their own tix token can
  read the task.

Nobody is told about their own actions, and each event gives a person at most one DM. `/tix notifications off`
turns these DMs off (`on` turns them back on); channel feeds are not affected.

## Task cards

With **Show task cards** on (System Console > Plugins > tix), a message that mentions a task such as
`infra-42` gets a card for it: up to three per message, matched against the project keys the author can see,
ignoring code and links. The card is coloured by the state's category and shows the title (linking to tix),
status, assignee, priority, due date, tags and the project.

By default (**Task card style**: *Reply in the thread*) the message stays as written and the tix bot answers it
in its thread with the cards, so the message is never marked edited. *Inline on the message* attaches the cards
to the message itself, as earlier versions did. A task that already has a card in the thread, or got one in the
channel within the **Card quiet period** (10 minutes by default), is not carded again. Editing a message to add
a task cards it; deleting the message deletes its card.

A card is drawn with the author's own tix token, so a message only shows tasks its author can see; an author who
has not run `/tix connect` gets no cards. The buttons (up to three next states from the workflow, *Assign to me*
and *Comment*) act with the token of whoever clicks them; a transition that needs a comment, and *Comment*, open a
dialog. Cards are redrawn in place after a button is used and when tix reports a change to the task.

**Thread sync.** A channel admin can run `/tix sync on` in a channel (`off` to stop, no argument to see the
state). Then a reply in a card's thread is added to the task in tix as a comment by whoever wrote it, with their
own token (people who have not connected get a one-time hint), and new tix comments on the task are posted into
the thread by the bot. Edits, bot and system posts are not mirrored, and a comment is never echoed back to the
thread or channel it came from. Everyone who can read the channel sees the mirrored comments.

## Development

To avoid having to manually install your plugin, build and deploy your plugin using one of the following options. In order for the below options to work, you must first enable plugin uploads via your config.json or API and restart Mattermost.

```json
    "PluginSettings" : {
        ...
        "EnableUploads" : true
    }
```

### Development guidance

1. Fewer packages is better: default to the main package unless there's good reason for a new package.

2. Coupling implies same package: don't jump through hoops to break apart code that's naturally coupled.

3. New package for a new interface: a classic example is the sqlstore with layers for monitoring performance, caching and mocking.

4. New package for upstream integration: a discrete client package for interfacing with a 3rd party is often a great place to break out into a new package

### Modifying the server boilerplate

The server code comes with some boilerplate for creating an api, using slash commands, accessing the kvstore and using the cluster package for jobs.

#### API package

`server/api` serves the plugin's HTTP routes. The `ServeHTTP` hook in plugin.go passes every request for `/plugins/{id}` to `api.Handler`, which registers its routes in `initRouter`. `NewHandler` takes what the handlers need (the plugin API, the channelstatus service, a getter for the webhook secret), so add new dependencies there. [Adding an HTTP endpoint](#adding-an-http-endpoint) and [Receiving webhooks](#receiving-webhooks) walk through the authenticated and unauthenticated patterns.

#### Channelstatus package

`server/channelstatus` holds the rules for reading and changing a status: the allowed values, the permission check, the audit record, and the WebSocket event that updates the channel's members. The slash command and the API both call it, so they can't drift apart. When a feature has more than one entry point, put its rules in one place like this.

#### Audit logging

`channelstatus.Service.Set` writes an audit record for every attempt to change a status, including invalid and denied ones. Copy its shape for any state change:

1. `plugin.MakeAuditRecordWithContext` starts a record with the event name, the acting user, the session from `plugin.Context` and the request path, marked as failed.
2. `defer p.API.LogAuditRec(rec)` writes it however the function returns.
3. `model.AddEventParameterToAuditRec` adds validated IDs and values. Never add message content, credentials or config.
4. `rec.Success()` runs only after the change is made.

Records go to the server's audit log, which is off by default. To see them locally, start the server with `MM_EXPERIMENTALAUDITSETTINGS_FILEENABLED=true` and `MM_EXPERIMENTALAUDITSETTINGS_FILENAME=audit.log`; each record is a JSON line in that file. Enterprise servers also show these settings under **System Console > Compliance > Audit Logging**. Changes need a server restart.

#### Command package

This package contains the boilerplate for adding a slash command and an instance of it is created in the `OnActivate` hook in plugin.go. `/channelstatus` shows or changes the channel's status through the channelstatus package. If you don't need it you can delete the package and remove any reference to `commandClient` in plugin.go. Run `make mock` to regenerate the mocks in `server/command/mocks` and `server/store/kvstore/mocks` after changing an interface.

#### KVStore package

This is a central place for you to access the KVStore methods that are available in the `pluginapi.Client`. The package contains an interface for you to define your methods that will wrap the KVStore methods. An instance of the KVStore is created in the `OnActivate` hook. Use `SetAtomicWithRetries` for read-modify-write updates; plugins can run on several nodes in a cluster at once.

### Modifying the webapp boilerplate

The webapp shows the current channel's status as a tag in the channel header and in a right-hand sidebar, where members with permission can change it. Statuses live in a small Redux reducer that a WebSocket handler keeps up to date.

- **Shared dependencies.** React, ReactDOM, react-intl, Redux, react-redux, react-router-dom and prop-types are provided by the Mattermost webapp at runtime (see `externals` in `webpack.config.js`). Keep their versions in `package.json` aligned with the server version you target. Don't bundle your own copies.
- **`@mattermost/shared`.** Imports from `@mattermost/shared/*` (e.g. `WithTooltip` from `@mattermost/shared/components/tooltip`) are loaded from the host at runtime. Install `@mattermost/shared` as a devDependency for the types when you first use it. Use its tooltip and emoji until the host adopts the Compass versions.
- **Compass.** UI is built with the [Compass design system](https://compass.mattermost.com) ([`@mattermost/compass-ui`](https://www.npmjs.com/package/@mattermost/compass-ui), [`@mattermost/compass-icons`](https://www.npmjs.com/package/@mattermost/compass-icons)). Browse available components on [compass.mattermost.com](https://compass.mattermost.com); view interactive examples and APIs in [Storybook](https://compass.mattermost.com/storybook/).
  - Use an existing Compass component before writing your own.
  - Import from subpaths (e.g. `@mattermost/compass-ui/components/button`), never the package root.
  - Import `@mattermost/compass-ui/styles` once in `index.tsx`, and never import `@mattermost/compass-ui/styles/standalone` in a plugin.
  - Wrap icons passed to Compass in `Icon` without a size: `leadingIcon={<Icon glyph={<RefreshIcon/>}/>}`.
  - Plain CSS is injected globally next to the host's and other plugins' styles, so prefix every class with your plugin's name (`.StarterTemplate-ChannelStatusSidebar`).
  - Take colors from the host theme variables (`--center-channel-color`, `--button-bg`, …), spacing, radius and type from Compass tokens (`--spacing-m`, `--radius-s`, `--font-size-100`, …).
- **API calls.** Use the helpers in `src/client.ts`. They go through `Client4.getOptions`, which adds the CSRF token the server requires on cookie-authenticated, non-GET requests.
- **Strings.** Add user-facing strings to `i18n/en.json` and render them with `react-intl`.
- **Tests.** Jest, React Testing Library and `userEvent`. See `channel_status_sidebar.test.tsx` for component tests against a real Redux store.

`webapp/AGENTS.md` has the full design, accessibility and testing rules, adapted from the Mattermost webapp's, and `server/AGENTS.md` has the server security and testing rules. Coding agents load them automatically when working in those folders.

### Adding a slash command

1. In `server/command/command.go`, add a trigger constant and register it in `NewCommandHandler`:
   ```go
   const versionCommandTrigger = "version"

   err = client.SlashCommand.Register(&model.Command{
       Trigger:          versionCommandTrigger,
       AutoComplete:     true,
       AutoCompleteDesc: "Show the plugin's version",
       AutocompleteData: model.NewAutocompleteData(versionCommandTrigger, "", "Show the plugin's version"),
   })
   ```
2. Add a `case versionCommandTrigger:` to the `switch` in `Handle` that calls your `executeVersionCommand(c, args, params)`.
3. Return `ephemeral(...)` so only the caller sees the reply. Use `model.CommandResponseTypeInChannel` only when everyone should see it.
4. The server only checks that the user can post in the channel. If a command changes anything, check the permission for what it changes, as `checkCanEdit` in `server/channelstatus` does. Use `HasPermissionTo(args.UserId, model.PermissionManageSystem)` only for system-wide actions.
5. Test it against a real server, as `TestChannelStatusEndpoint` in `server/api/api_test.go` does. Use mocks only for failures a real server can't produce, like `TestNewCommandHandler`.

Commands are registered when the plugin activates, so redeploy to pick up a new one.

### Adding an HTTP endpoint

1. Add a route to `apiRouter` in `initRouter` (`server/api/api.go`). Routes there require a logged-in user. Use GET only for reads; anything that changes state uses POST, PUT, PATCH or DELETE.
2. In the handler, read the user from `r.Header.Get("Mattermost-User-ID")`, validate every ID with `model.IsValidId`, and check permissions (`HasPermissionToChannel`, `HasPermissionToTeam`, `HasPermissionTo`) before touching data. If a slash command or webhook does the same thing, put the check in a shared service as `channelstatus` does.
3. Respond with `writeJSON`, and report failures with `writeError` so internal errors are logged rather than sent to the user.
4. For state-changing routes, write an audit record as `Set` in `server/channelstatus` does.
5. Call it from the webapp through a function in `webapp/src/client.ts`, which adds the CSRF token.
6. In `server/api/api_test.go`, test the allowed and the denied case with `httptest`, and the real permission check against a server with `testhelper`.

The endpoint is served at `<SiteURL>/plugins/<plugin-id>/api/v1/...`.

### Receiving webhooks

`POST /webhook` (`handleWebhook` in `server/api/api.go`) is the starting point for calls from external services. They have no Mattermost session, so the route sits outside `apiRouter` and checks a shared secret instead. This is the pattern the GitHub and GitLab plugins use.

Nothing has to be registered with Mattermost: the route exists as soon as the plugin is enabled. If all you need is for an external service to post messages, Mattermost's built-in incoming webhooks (Integrations > Incoming Webhooks) do that without a plugin. Use a plugin route when you need to process the payload yourself.

1. An admin clicks **Regenerate** next to **Webhook Secret** in System Console > Plugins > <your plugin>, then **Save**, and gives the external service that value and the URL `<SiteURL>/plugins/<plugin-id>/webhook`.
2. The service sends the secret in the `X-Webhook-Secret` header. The handler compares it in constant time and rejects everything while no secret is configured.
3. Decode the payload where the handler says `Handle the payload here`, e.g. `json.NewDecoder(r.Body).Decode(&payload)`. `limitRequestBody` already caps its size.
4. If the provider signs its requests (GitHub, Stripe, Slack, ...), verify that HMAC signature over the raw body instead of, or as well as, the shared secret.
5. To post the result in a channel, create a bot with `p.client.Bot.EnsureBot` in `OnActivate` and post as that bot.

Regenerating the secret and saving invalidates the old one immediately.

### Adding System Console settings

Settings declared in `plugin.json` appear automatically under System Console > Plugins > <your plugin>.

1. Add an entry to `settings_schema.settings` in `plugin.json`:
   ```json
   {
       "key": "AllowInDirectMessages",
       "display_name": "Allow statuses in direct messages:",
       "type": "bool",
       "help_text": "Let members of direct and group messages set a status.",
       "default": true
   }
   ```
   `type` is one of `bool`, `text`, `longtext`, `number`, `dropdown`, `radio`, `generated`, `username` or `custom`. Add `"secret": true` to credentials and keys so they're masked in the System Console and support packets.
2. Add a field with the same name to the `configuration` struct in `server/configuration.go` (`AllowInDirectMessages bool`).
3. Read it with `p.getConfiguration().AllowInDirectMessages`. `OnConfigurationChange` reloads the struct whenever an admin saves.
4. To reject invalid values before they're saved, implement the `ConfigurationWillBeSaved` hook.

### Adding webapp UI

Register components in `initialize` in `webapp/src/index.tsx`. The template uses:

- `registry.registerRightHandSidebarComponent(Component, title)` for the sidebar. It returns `showRHSPlugin` and `toggleRHSPlugin`, actions that open it and toggle it.
- `registry.registerChannelHeaderButtonAction(icon, action, dropdownText, tooltipText)` for the App Bar button that dispatches `toggleRHSPlugin`.
- `registry.registerChannelHeaderIcon(Component)` for the status tag. It renders after the pinned messages and files buttons and receives the `channel` (11.5+).
- `registry.registerReducer(reducer)` for plugin state, available under `state['plugins-<plugin-id>']`.
- `registry.registerWebSocketEventHandler('custom_<plugin-id>_<event>', handler)` to receive events the server publishes with `p.client.Frontend.PublishWebSocketEvent`, and `registry.registerReconnectHandler` to refetch what was missed while offline.

Other surfaces (post menu actions, custom post types, root components, admin console sections, ...) are listed in the [webapp plugin reference](https://developers.mattermost.com/extend/plugins/webapp/reference/). Put each component in its own folder under `src/components/` with its CSS and test, and use Compass components for the UI.

### Security checklist

For requests to `/plugins/{id}`, the Mattermost server strips any spoofed `Mattermost-User-Id`, sets it when the request has a valid session, and enforces CSRF on cookie-authenticated, non-GET requests. It still passes requests without a session to your plugin: `apiRouter` rejects them, and any route outside it must verify its caller. Everything else is up to your plugin.

**Authentication and authorization**

- **Authorize every request.** A logged-in user isn't necessarily allowed to see a channel, team or post. Check with `p.client.User.HasPermissionTo`, `HasPermissionToTeam` or `HasPermissionToChannel` before reading or changing data the user names in the request. Take the user's identity only from the `Mattermost-User-Id` header.
- **Enforce permissions on the server.** Hiding a button in the webapp doesn't stop anyone from calling the API.
- **Scope KV data.** The KV store is shared by all users of the plugin; key per-user data by user ID and check ownership.
- **Check slash command permissions.** The server only checks that the user can post in the channel; admin-only subcommands must check more.

**Requests that don't come from a logged-in user**

- **Authenticate webhooks.** Routes outside `MattermostAuthorizationRequired` must verify the caller themselves, e.g. with a shared secret compared using `crypto/subtle.ConstantTimeCompare`, or the provider's HMAC signature. Fail closed when the secret isn't configured, and never accept secrets in the query string.
- **Allowlist inter-plugin callers.** Requests from other plugins have no user ID; check `Mattermost-Plugin-ID` against the plugins you expect.
- **Treat interactive actions and dialogs as user input.** Re-check permissions, and verify the post really came from your bot before trusting its context.
- **Don't trust post props.** Clients can set most of them; identify your own posts by `post.UserId`.
- **Keep `public/` public.** Anything in it is served to anyone without logging in.

**Input, output and errors**

- **Validate input.** Check IDs with `model.IsValidId`, cap request bodies (see `limitRequestBody`), paginate lists, and reject unknown values.
- **Never change state on GET.** CSRF checks only cover POST, PUT, PATCH and DELETE.
- **Avoid injection.** Use SQL placeholders, never pass user input to `os/exec`, and validate file paths with `filepath.IsLocal`.
- **Limit outbound requests.** Set timeouts, and allowlist the hosts for any user-supplied URL so the plugin can't be used to reach internal services.
- **Don't leak internals.** Log the underlying error and return a generic message (see `writeError`).

**Secrets, logging and auditing**

- **Keep secrets secret.** Mark settings `"secret": true` in `plugin.json` so they're masked in the System Console and support packets. Use `"type": "generated"` for values the plugin creates itself. Encrypt per-user OAuth tokens before storing them.
- **Log carefully.** Never log tokens, secrets, query strings or message content.
- **Audit state changes.** Write an audit record for every state-changing request, including denied ones, with IDs and validated values only (see `Set` in `server/channelstatus`).
- **Scope WebSocket events.** Everyone who receives an event can read its payload. Broadcast to a channel or user (`model.WebsocketBroadcast{ChannelId: ...}`), never to everyone, unless the data is public.

**Webapp**

- **No raw HTML.** `dangerouslySetInnerHTML` is blocked by ESLint; render Markdown with `window.PostUtils.formatText` and `messageHtmlToComponent`, which sanitize it.
- **Check URLs.** Allow only `http:` and `https:` in user-supplied `href` and `src` values.
- **Keep credentials on the server.** Don't store tokens in `localStorage` or call third-party APIs with credentials from the browser.
- **Check message origins.** Verify `event.origin` in `message` event listeners.

**Dependencies**

- **Stay patched.** Dependabot (`.github/dependabot.yml`) opens monthly update PRs. Run `govulncheck ./...` and `npm audit --omit=dev` in `webapp/` before releasing.
- **Watch the scans.** CodeQL checks the Go and TypeScript code and the GitHub workflows on every PR, and OpenSSF Scorecard checks the repository's supply-chain settings weekly. Results appear under the repository's Security > Code scanning tab. Outside the `mattermost` organization both workflows are skipped, because code scanning on a private repository needs GitHub Code Security. To turn them on, add a repository variable `ENABLE_CODE_SCANNING` set to `true` (Settings > Secrets and variables > Actions > Variables).

### Deploying with Local Mode

If your Mattermost server is running locally, you can enable [local mode](https://docs.mattermost.com/administration/mmctl-cli-tool.html#local-mode) to streamline deploying your plugin. Edit your server configuration as follows:

```json
{
    "ServiceSettings": {
        ...
        "EnableLocalMode": true,
        "LocalModeSocketLocation": "/var/tmp/mattermost_local.socket"
    },
}
```

and then deploy your plugin:
```
make deploy
```

You may also customize the Unix socket path:
```bash
export MM_LOCALSOCKETPATH=/var/tmp/alternate_local.socket
make deploy
```

If developing a plugin with a webapp, watch for changes and deploy those automatically:
```bash
export MM_SERVICESETTINGS_SITEURL=http://localhost:8065
export MM_ADMIN_TOKEN=j44acwd8obn78cdcx7koid4jkr
make watch
```

### Deploying with credentials

Alternatively, you can authenticate with the server's API with credentials:
```bash
export MM_SERVICESETTINGS_SITEURL=http://localhost:8065
export MM_ADMIN_USERNAME=admin
export MM_ADMIN_PASSWORD=password
make deploy
```

or with a [personal access token](https://docs.mattermost.com/developer/personal-access-tokens.html):
```bash
export MM_SERVICESETTINGS_SITEURL=http://localhost:8065
export MM_ADMIN_TOKEN=j44acwd8obn78cdcx7koid4jkr
make deploy
```

### Releasing new versions

The version of a plugin is determined at compile time, automatically populating a `version` field in the [plugin manifest](plugin.json):
* If the current commit matches a tag, the version will match after stripping any leading `v`, e.g. `1.3.1`.
* Otherwise, the version will combine the nearest tag with `git rev-parse --short HEAD`, e.g. `1.3.1+d06e53e1`.
* If there is no version tag, an empty version will be combined with the short hash, e.g. `0.0.0+76081421`.

To disable this behaviour, manually populate and maintain the `version` field.

## How to Release

To trigger a release, follow these steps:

1. **For Patch Release:** Run the following command:
    ```
    make patch
    ```
   This will release a patch change.

2. **For Minor Release:** Run the following command:
    ```
    make minor
    ```
   This will release a minor change.

3. **For Major Release:** Run the following command:
    ```
    make major
    ```
   This will release a major change.

4. **For Patch Release Candidate (RC):** Run the following command:
    ```
    make patch-rc
    ```
   This will release a patch release candidate.

5. **For Minor Release Candidate (RC):** Run the following command:
    ```
    make minor-rc
    ```
   This will release a minor release candidate.

6. **For Major Release Candidate (RC):** Run the following command:
    ```
    make major-rc
    ```
   This will release a major release candidate.

## Q&A

### How do I make a server-only or web app-only plugin?

Simply delete the `server` or `webapp` folders and remove the corresponding sections from `plugin.json`. The build scripts will skip the missing portions automatically.

### How do I include assets in the plugin bundle?

Place them into the `assets` directory. To use an asset at runtime, build the path to your asset and open as a regular file:

```go
bundlePath, err := p.API.GetBundlePath()
if err != nil {
    return errors.Wrap(err, "failed to get bundle path")
}

profileImage, err := os.ReadFile(filepath.Join(bundlePath, "assets", "profile_image.png"))
if err != nil {
    return errors.Wrap(err, "failed to read profile image")
}

if appErr := p.API.SetProfileImage(userID, profileImage); appErr != nil {
    return errors.Wrap(appErr, "failed to set profile image")
}
```

### How do I build the plugin with unminified JavaScript?
Setting the `MM_DEBUG` environment variable will invoke the debug builds. The simplist way to do this is to simply include this variable in your calls to `make` (e.g. `make dist MM_DEBUG=1`).
