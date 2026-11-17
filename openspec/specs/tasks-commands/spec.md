# tasks-commands Specification

## Purpose

The `/tix` subcommands for working tasks from Mattermost: create, show, list, search, move through the
workflow, comment and assign, a channel's default project, and creating a task by reacting to a message. Every call acts as the person who typed the command, through the tix token they linked
with `/tix connect`, so tix applies that person's permissions and records them as the author.

## Requirements

### Requirement: Commands act as the linked user

Every task command SHALL call tix with the caller's own linked token and SHALL reply only to the caller
unless they ask for a channel post.

#### Scenario: Caller is not connected

- **WHEN** a person who has not run `/tix connect` runs `/tix list`
- **THEN** the plugin replies ephemerally that they must run `/tix connect`, and calls no tix route

#### Scenario: tix refuses the call

- **WHEN** tix answers a task command with 401, 403 or 404
- **THEN** the plugin replies with a short line for that status (reconnect, not allowed with tix's reason, not found) and no internal detail

### Requirement: Create a task

`/tix new [project] <title...>` SHALL create a task with tix's quick-add parsing on, so `!priority`,
`#tag`, `@handle`, `due:` and `+project` tokens in the title set those fields. The first word SHALL be
taken as the project only when it is the key of a project the caller can see and a title follows it.
`--assign @user|me|<handle>`, `--priority <name|1-5>`, `--project <key>`, `--raw` (title kept verbatim)
and `--post` SHALL be accepted.

#### Scenario: Project and quick-add tokens

- **WHEN** a person runs `/tix new infra Rotate keys !high #ops`
- **THEN** tix receives `project_ref` infra, the title `Rotate keys !high #ops` and `parse` true, and the person sees the new task's reference linked to tix with its status, assignee, priority, due date and tags

#### Scenario: A Mattermost mention becomes the assignee

- **WHEN** the title or `--assign` names `@bob` and the Mattermost user bob has linked tix
- **THEN** the task is assigned to bob's tix actor and `@bob` is removed from the title

#### Scenario: Mentioned user is not linked

- **WHEN** the title names `@carol`, a Mattermost user who has not linked tix
- **THEN** no task is created and the reply says carol has not connected tix

#### Scenario: Channel default project

- **WHEN** a person runs `/tix new <title>` without a project in a channel whose default project is infra
- **THEN** tix receives `project_ref` infra; a project named on the line or with `--project` wins over the default

#### Scenario: Announce in the channel

- **WHEN** the command carries `--post`
- **THEN** the bot posts "@person created <ref>: <title>" with the task's facts in the channel (in the thread when run from one) instead of an ephemeral reply

### Requirement: Show a task

`/tix show <ref>` SHALL reply with the task's title linked to tix, its status label from the project's
workflow, assignee (an @mention when linked), priority, due date (marked overdue when past), tags, an excerpt
of its description and its last three comments, in the caller's Mattermost language. Due dates SHALL follow the
DateFormat setting. A due_at of exactly midnight UTC SHALL be read as a bare date, the same calendar day in every
time zone, and is overdue only once that day is over in the caller's Mattermost time zone; any other due_at is
overdue once it has passed.

#### Scenario: Due today, created in the morning

- **WHEN** a person in Europe/Sofia runs `/tix new infra Pay the bill due:today` at 09:00
- **THEN** the reply shows today's date as the due date and does not say overdue

#### Scenario: Task with many comments

- **WHEN** a task has four comments
- **THEN** the reply shows the last three with their authors and ages, and says "last 3 of 4"

### Requirement: List tasks

`/tix list` SHALL default to the caller's open tasks (assigned to them, not in a terminal state), grouped by
status label in the order in progress, blocked, waiting, to do, done, cancelled. A first word that is a
project key SHALL list that project's open tasks; `mine` SHALL restrict to the caller; `all` SHALL include
finished tasks. Further words SHALL be tix filter terms (`project:`, `status:`, `tag:`, `assignee:`, `@user`,
`creator:`, `claimed-by:`, `priority:`, `title~`, `body~`, `text~`, `due:`, `due-before:`, `due-after:`,
`is:`, `sort:`, `limit:` and bare words, each negatable with a leading `-` where tix allows it) carried as
GET /tasks query parameters. A listing SHALL print at most 50 tasks and say when there are more.

#### Scenario: Default listing

- **WHEN** a person runs `/tix list`
- **THEN** tix is asked for `assignee=me` with `not_status` set to the keys that are terminal in every workflow, finished tasks are dropped, and the reply groups the rest by status

#### Scenario: A status term widens to finished tasks

- **WHEN** a person runs `/tix list status:done tag:ops`
- **THEN** tix is asked for `status=done&tag=ops` and no open-only filter is added

#### Scenario: Unknown term

- **WHEN** a filter term has an unknown key, or `~` on a key other than title, body or text
- **THEN** the reply names the term and nothing is fetched

### Requirement: Search, mine and due

`/tix search <text...> [project] [filter terms...] [open]` SHALL send the bare words as GET /tasks `q` (tix's
full-text search over title and body) with the filter terms of `/tix list`; a bare word after the first that is
a visible project key SHALL pick that project. A search SHALL include finished tasks unless `open` is given,
and SHALL print at most 15 tasks, each linked, with its status label and assignee. `/tix mine [filter]` SHALL be
`/tix list mine [filter]`. `/tix due [project] [filter]` SHALL list the caller's open tasks due by the end of
today in their Mattermost time zone, overdue ones included, sorted by `due_at` ascending, at most 15; a bare
due date of tomorrow SHALL NOT be listed. The `due:today` and `due:overdue` filter terms SHALL use the same
calendar rule.

#### Scenario: Search without text

- **WHEN** a person runs `/tix search tag:ops`
- **THEN** the reply asks what to look for and nothing is fetched

#### Scenario: Open search

- **WHEN** a person runs `/tix search keys open`
- **THEN** tix is asked for `q=keys` with the open-only `not_status` filter, and finished tasks are dropped

#### Scenario: Due today

- **WHEN** a person whose time zone is UTC runs `/tix due` on 17.11.2026
- **THEN** tix is asked for the caller's actor as `assignee`, `due_before=2026-11-17T23:59:59Z`, `sort=due_at` and `direction=asc`

#### Scenario: Due today in another time zone

- **WHEN** a person in Europe/Sofia runs `/tix due` at 09:00 local time
- **THEN** tix is asked for `due_before=2026-11-17T21:59:59Z`, and a bare due date of 18.11.2026 that tix returns is dropped

### Requirement: Channel default project

`/tix channel` SHALL show the channel's default project. `/tix channel project <key>` SHALL set it and
`/tix channel project none` SHALL clear it, only for a person holding the channel's manage-roles permission
(channel, team and system admins), checked on the server; the key SHALL be a project the person can see in
tix. A change SHALL write the audit record `tixSetChannelProject` with the channel id and project key. The
default SHALL be readable, through one check, only by people who can read the channel: by `/tix new`, the
create reaction, the project autocomplete (which lists it first, marked as the channel's default) and the
webapp's `GET /api/v1/me?channel_id=`.

#### Scenario: Member tries to set it

- **WHEN** a channel member who is not a channel admin runs `/tix channel project infra`
- **THEN** the reply says only channel admins can change it and nothing is stored

#### Scenario: Unknown project

- **WHEN** a channel admin names a project they cannot see in tix
- **THEN** the reply says so and nothing is stored

### Requirement: Create a task from a reaction

Reacting to a message with the emoji of the `CreateEmoji` setting (default `ticket`, colons optional) SHALL
create a task as the reacting person, through their own token, in the channel's default project, with the
message's first non-empty line (trimmed, at most 120 characters) as the title, quick-add parsing off, and the
message plus a permalink to it as the body. The bot SHALL then reply in the message's thread
"@person created <ref>", with the reference in the post prop `tix_created_ref`. One task SHALL be made per
message and emoji: the first reaction claims the message atomically in the KV store and records the reference.
Reactions by the bot, by people who cannot read the channel, on system messages and with other emoji SHALL be
ignored. Nothing SHALL remove the reaction.

#### Scenario: Reactor not connected

- **WHEN** a person who has not linked tix reacts with the emoji
- **THEN** they get an ephemeral hint to run `/tix connect`, no task is created and the reaction stays

#### Scenario: No default project

- **WHEN** the channel has no default project
- **THEN** the reactor gets an ephemeral asking a channel admin to run `/tix channel project <key>` or to use **Create tix task** from the message menu, and no task is created

#### Scenario: Second reaction

- **WHEN** anyone reacts with the emoji to a message that already has a task
- **THEN** no task is created and they get an ephemeral linking the existing reference

#### Scenario: Two reactions at once

- **WHEN** a second reaction arrives while the first is still creating the task
- **THEN** the second gets an ephemeral that the task is being created; a claim older than two minutes is taken over

### Requirement: Move along the workflow

`/tix start`, `/tix done`, `/tix close`, `/tix reopen` and `/tix move <ref> <state>` SHALL resolve the target
from the task's project workflow: start to a state of category in_progress, done to category done, close to a
terminal cancelled state (done when the workflow has none), reopen to a non-terminal state preferring the
initial one, and move to the state named by key or label. A state without a category SHALL be treated as tix
derives it (terminal is done, initial is todo, otherwise in progress). Only a transition the workflow allows
directly from the current state SHALL be sent; text after the reference (after the state for move) SHALL be
sent as the transition comment.

#### Scenario: Direct transition

- **WHEN** a task is in To do and the workflow allows To do to Doing, and a person runs `/tix start <ref>`
- **THEN** tix receives a transition to `doing` and the reply shows "To do → Doing"

#### Scenario: Already there

- **WHEN** `/tix done` targets a task already in a done state, or `/tix close` one in any terminal state
- **THEN** the reply says the task is already in that state and no transition is sent

#### Scenario: No direct transition

- **WHEN** the goal state is not reachable in one step
- **THEN** no transition is sent, and the reply lists the states reachable from the current one and, when one exists, the shortest route with the `/tix move` command for its first hop

#### Scenario: Transition requires a comment

- **WHEN** the chosen transition has `requires_comment` and no comment was given
- **THEN** no transition is sent and the reply shows the command to repeat with a comment

### Requirement: Comment and assign

`/tix comment <ref> <text...>` SHALL add the raw text after the reference, line breaks and quotes kept, as a
comment. `/tix assign <ref> <who>` SHALL set the assignee: `me` is the caller, `none` unassigns, `@user` is
that Mattermost user's linked tix actor, and any other word is passed to tix as a handle.

#### Scenario: Unassign

- **WHEN** a person runs `/tix assign infra-2 none`
- **THEN** tix receives `assignee_actor_id` set to the empty string and the reply says the task is unassigned

### Requirement: Autocomplete lists

The plugin SHALL serve dynamic autocomplete lists under `/api/v1/autocomplete/` for references (the caller's
open assigned tasks, then open tasks they created, most recently updated first), project keys (the channel's default first, with `mine`
and `all` for `/tix list` and `none` for `/tix channel project`) and the states the referenced task can move to, marking those that need a comment.
The routes SHALL take the user only from the `Mattermost-User-ID` header and SHALL answer an empty list when
the user is not linked or tix fails.

#### Scenario: No session

- **WHEN** an autocomplete route is called without `Mattermost-User-ID`
- **THEN** it answers 401

#### Scenario: States for move

- **WHEN** the typed line is `/tix move infra-2 ` and infra-2 is in review
- **THEN** the list holds the states the workflow allows from review, with "(needs a comment)" on those that require one
