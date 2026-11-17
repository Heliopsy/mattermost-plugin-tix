# webapp Specification

## Purpose

The webapp lets a person work their tix tasks without leaving Mattermost: a right-hand sidebar with their tasks and a project's tasks, a task view to move, reassign, reprioritise and comment, and a modal that creates a task from a post or from scratch. It talks only to the plugin's `/api/v1` routes (`server/webapi`), which act as the signed-in Mattermost user through that person's own tix token.

## Requirements

### Requirement: Acting as the signed-in user
Every webapi route SHALL require the `Mattermost-User-ID` header the server sets for an authenticated session, and SHALL call tix only with the token linked to that user.

#### Scenario: No session
- **WHEN** a request reaches `/api/v1/tasks` without `Mattermost-User-ID`
- **THEN** the plugin answers 401 and does not call tix

#### Scenario: Linked user
- **WHEN** a linked user lists tasks
- **THEN** the plugin calls tix `GET /tasks` with that user's token

### Requirement: Not-linked answer
A route that needs tix SHALL answer 409 with `{"error": ..., "code": "not_linked"}` when the user has not connected, and 409 with code `token_rejected` when tix rejects the stored token, and the webapp SHALL show a connect prompt for either.

#### Scenario: Unlinked user opens the sidebar
- **WHEN** an unlinked user opens the tix sidebar
- **THEN** the sidebar shows "Connect to tix", with a Connect button when the admin token allows connecting by email and otherwise the instruction to run `/tix connect`

#### Scenario: Connect by email
- **WHEN** the user presses Connect and tix has a person with their verified email
- **THEN** `POST /api/v1/connect` links them the same way `/tix connect` does and the sidebar loads their tasks

#### Scenario: No matching person
- **WHEN** no tix person has the user's verified email
- **THEN** `POST /api/v1/connect` answers 409 with code `no_match` and the prompt shows the message

### Requirement: Task listing
`GET /api/v1/tasks` SHALL pass `mine`, `open`, `project`, `q`, `status`, `cursor` and `limit` through to tix `GET /tasks`, SHALL cap `limit` at 100, and SHALL return each task with its project key, workflow state (label, category, terminal) and assignee resolved to a Mattermost user when linked.

#### Scenario: My open tasks
- **WHEN** the webapp asks for `mine=true&open=true`
- **THEN** the plugin asks tix for `assignee=me` and leaves out the terminal states of the workflows with `not_status`

#### Scenario: Unknown project
- **WHEN** `project` names a project the user cannot see
- **THEN** the plugin answers 404

### Requirement: Sidebar
The sidebar SHALL have a "Mine" tab listing the user's open tasks grouped by status category (in progress, to do, blocked, waiting), a "Project" tab with a project picker and a search, and loading, empty and error states.

#### Scenario: Open a task
- **WHEN** the user selects a task in a list
- **THEN** the sidebar shows the task with its status pill, assignee, priority, tags, description rendered as Markdown by the host, comments, and one button per transition the workflow allows from its state

### Requirement: Transitions honour required comments
A transition whose workflow edge has `requires_comment` SHALL NOT be sent until the user writes a comment, which is sent with the move.

#### Scenario: Move that needs a comment
- **WHEN** the user picks a transition that requires a comment
- **THEN** the sidebar shows a comment box and the confirm button stays disabled until the comment is not empty

### Requirement: Assignee by Mattermost user
Create and update SHALL take the assignee as a Mattermost user id and map it to the tix actor that user connected as; an empty id on update SHALL unassign.

#### Scenario: Assignee not connected
- **WHEN** the chosen assignee has not connected Mattermost to tix
- **THEN** the plugin answers 422 with code `assignee_not_linked` and creates nothing

#### Scenario: Assign to a connected user
- **WHEN** the user creates a task assigned to a connected Mattermost user
- **THEN** tix receives that user's actor id as `assignee_actor_id`

### Requirement: Create from a post
The post menu SHALL offer "Create tix task", which opens a modal with the post's first line as the title and its message plus a permalink as the description, a project picker, the assignee (the user by default) and a priority.

#### Scenario: Created from a post
- **WHEN** the user creates the task
- **THEN** the modal shows the new reference linked to tix and offers "Post link in thread", which posts a reply as the user containing the reference so task cards can unfurl it

### Requirement: Channel default project in the modal
The modal SHALL ask `GET /api/v1/me?channel_id=<channel>` for the channel's default project and preselect it when the user can see that project, falling back to the project last used in the modal and then the first one. The plugin SHALL report `default_project` only to a user who can read that channel.

#### Scenario: Channel with a default
- **WHEN** the channel's default project is ops and the user opens the modal in it
- **THEN** the project picker starts on ops

#### Scenario: Not a member
- **WHEN** a user asks `/me` with the id of a channel they cannot read
- **THEN** the answer has no `default_project`

### Requirement: Global create
The main menu and the sidebar SHALL offer creating a task without a post, using the same modal, which then offers posting the link in the current channel.

#### Scenario: Create from the main menu
- **WHEN** the user picks "Create tix task" in the main menu
- **THEN** the modal opens with empty title and description for the current channel

### Requirement: Validation and errors
The plugin SHALL reject a missing or overlong title, a priority outside 1 to 5, malformed task references, project keys and user ids with 400, SHALL pass tix's own refusals (400, 403, 404, 409, 412, 422) through with tix's message, and SHALL answer 502 with a generic message when tix fails otherwise.

#### Scenario: tix is down
- **WHEN** tix answers 500 or cannot be reached
- **THEN** the plugin answers 502 without tix's response body

### Requirement: Audit
Create, update, transition, comment and connect SHALL write a Mattermost audit record with the task reference and never the content.

#### Scenario: Comment audited
- **WHEN** a user comments on infra-1 from the sidebar
- **THEN** an audit record `tixAddComment` with `task_ref` infra-1 is logged with status success
