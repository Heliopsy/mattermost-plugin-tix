# i18n Specification

## Purpose

The plugin speaks each person's Mattermost language. The server and the webapp carry English and Bulgarian
message catalogs; every user-facing text comes from them, with English as the fallback, and tests keep the
catalogs complete and consistent.

## Requirements

### Requirement: Server messages by message id
Every user-facing server text (command replies, help, errors, card labels and buttons, dialogs, feed lines, DMs,
the digest and the autocomplete help) SHALL be rendered from a message id in `assets/i18n/<language>.json`,
embedded in the server binary. A message SHALL be a string with `{name}` placeholders, or an object of CLDR
plural forms (`one`, `other`) for text with a count, where English and Bulgarian use `one` for exactly 1. A
message missing in a language SHALL fall back to English, and a missing id SHALL render as the id. Slash command
names, option keywords and filter syntax SHALL stay English in every language; their help text SHALL be
translated. Workflow state labels SHALL be shown as tix sends them.

#### Scenario: Plural
- **WHEN** the digest lists 11 overdue tasks for a Bulgarian reader
- **THEN** the tail line reads "…и още 1" and for 12 tasks "…и още 2", from the `one` and `other` forms

#### Scenario: Unknown language
- **WHEN** a person's Mattermost language is Portuguese
- **THEN** they get English text

### Requirement: Whose language
A reply only one person sees (an ephemeral command reply, a button reply, a dialog and its errors, a webapp API
error) SHALL be in that person's Mattermost language. A DM SHALL be in its recipient's language. A post many
people read (a channel feed post, a task card, a bot post in a channel or thread) SHALL be in the server's default
language: Default Client Language under System Console > Localization, else Default Server Language, else
English. The `/tix` autocomplete is registered once at activation for everyone and SHALL use that server default
language.

#### Scenario: Ephemeral reply
- **WHEN** a person whose language is Bulgarian runs `/tix sync`
- **THEN** the ephemeral reply is in Bulgarian, whatever the server's default language

#### Scenario: Card in a channel
- **WHEN** a Bulgarian person mentions infra-42 in a channel of an English server
- **THEN** the card's fields and buttons are in English

### Requirement: Complete catalogs
Tests SHALL fail when code names a message id that `en.json` lacks, when a language lacks an id `en.json` has,
when a message's placeholders or plural forms differ between languages, or when the webapp uses an id its
`en.json` lacks or whose `defaultMessage` differs from it. A message only one catalog has SHALL only be reported
as a warning.

#### Scenario: Missing translation
- **WHEN** a developer adds a server message to `en.json` but not to `bg.json`
- **THEN** `go test ./server/...` fails naming the id
