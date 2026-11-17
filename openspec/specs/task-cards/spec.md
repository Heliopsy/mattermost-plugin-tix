# task-cards Specification

## Purpose

Turn task references in Mattermost messages into live tix task cards with buttons that move, assign and
comment on the task, without showing a channel anything the message's author cannot see in tix.

## Requirements

### Requirement: Unfurl task references

When the UnfurlRefs setting is on, the plugin SHALL attach a task card to a new message for each task
reference it mentions, at most three per message. A reference SHALL be `<project key>-<number>` where the key
is a project the author can see in tix, matched case-insensitively and as a whole word. References inside
code blocks, inline code and URLs SHALL be ignored.

#### Scenario: Message mentions a task
- **WHEN** a connected user posts "please look at infra-42" and infra is one of their projects
- **THEN** the post carries a card for infra-42 and lists infra-42 in its `tix_refs` prop

#### Scenario: Reference in code or a URL
- **WHEN** a message mentions infra-42 only inside `inline code`, a fenced block or a link
- **THEN** no card is attached

#### Scenario: Unknown project key
- **WHEN** a message mentions ops-12 and the author can see no project with key ops
- **THEN** no card is attached

#### Scenario: Unfurling switched off
- **WHEN** UnfurlRefs is off
- **THEN** no message gets a card

### Requirement: Cards are drawn with the author's own access

The plugin SHALL read a task for a card with the tix token of the post's author. When the author is not
connected to tix or cannot see the task, the plugin SHALL NOT attach a card for it. A message SHALL NOT be able
to bring its own cards: attachments with buttons aimed at the plugin and a `tix_refs` prop supplied by the
poster SHALL be removed before the message is saved.

#### Scenario: Author cannot see the task
- **WHEN** a user mentions a task that tix does not show them
- **THEN** the post gets no card for it, even if other channel members could see the task

#### Scenario: Author not connected
- **WHEN** a user who has not run `/tix connect` mentions a task
- **THEN** the post gets no card

#### Scenario: Forged card
- **WHEN** a message arrives with an attachment whose buttons target the plugin
- **THEN** that attachment and any `tix_refs` prop are removed and other attachments are kept

### Requirement: Card content

A card SHALL be coloured by the category of the task's state (todo grey, in_progress blue, blocked and
waiting amber, done green, cancelled dark), SHALL be titled with the reference and title linking to the task
in tix, SHALL show the status label, assignee, priority, due date and tags when set, and SHALL carry the footer
`tix · <project name>`.

#### Scenario: Task in progress
- **WHEN** a card is drawn for a task in a state of category in_progress
- **THEN** the card is blue and its Status field shows the state's label

### Requirement: Card buttons

A card SHALL offer up to three transitions the workflow allows from the task's current state, the ones that
move work forward first, plus "Assign to me" while the task is not finished and "Comment". A button SHALL act
with the tix token of the person who clicks it, after checking that they can read the post's channel and that
the post shows that task. A person who is not connected SHALL get an ephemeral message pointing to
`/tix connect`.

#### Scenario: Unconnected user clicks a button
- **WHEN** a user who has not connected to tix clicks Done on a card
- **THEN** they see an ephemeral message telling them to run `/tix connect` and nothing changes in tix

#### Scenario: Transition that needs a comment
- **WHEN** a user clicks a transition the workflow marks requires_comment
- **THEN** a dialog asks for the comment and the move is made, with the comment, only when it is submitted

#### Scenario: Comment
- **WHEN** a user clicks Comment and submits text
- **THEN** the comment is added to the task as that user

#### Scenario: Assign to me
- **WHEN** a connected user clicks "Assign to me"
- **THEN** the task is assigned to that user's tix actor

### Requirement: Cards stay current

After a button or dialog changes a task, the plugin SHALL redraw the card in place and every other post showing
that task. `Refresh(ref)` SHALL redraw every post recorded for the reference with the token of each post's
author, skipping posts whose author is not connected, dropping the card of a task the author can no longer see,
and forgetting deleted posts.

#### Scenario: Task changes in tix
- **WHEN** notify reports that infra-42 moved to done
- **THEN** every card of infra-42 shows Done and is green

#### Scenario: Post deleted
- **WHEN** a post that showed a card has been deleted
- **THEN** Refresh stops tracking it
