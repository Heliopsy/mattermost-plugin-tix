# tasks-commands Specification

## Purpose

The `/tix` subcommands for working tasks from Mattermost: create, show, list, move through the workflow,
comment and assign. Every call acts as the person who typed the command, through the tix token they linked
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

#### Scenario: Announce in the channel

- **WHEN** the command carries `--post`
- **THEN** the bot posts "@person created <ref>: <title>" with the task's facts in the channel (in the thread when run from one) instead of an ephemeral reply

### Requirement: Show a task

`/tix show <ref>` SHALL reply with the task's title linked to tix, its status label from the project's
workflow, assignee (an @mention when linked), priority, due date (marked overdue when past), tags, an excerpt
of its description and its last three comments.

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
open assigned tasks, then open tasks they created, most recently updated first), project keys (with `mine`
and `all` for `/tix list`) and the states the referenced task can move to, marking those that need a comment.
The routes SHALL take the user only from the `Mattermost-User-ID` header and SHALL answer an empty list when
the user is not linked or tix fails.

#### Scenario: No session

- **WHEN** an autocomplete route is called without `Mattermost-User-ID`
- **THEN** it answers 401

#### Scenario: States for move

- **WHEN** the typed line is `/tix move infra-2 ` and infra-2 is in review
- **THEN** the list holds the states the workflow allows from review, with "(needs a comment)" on those that require one
