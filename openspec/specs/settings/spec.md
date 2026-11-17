# settings Specification

## Purpose

System Console settings that shape how the plugin writes things: feed detail, comment text, people in feeds,
task card fields and buttons, the date format, the digest's sections and the bot's name and icon. One package
(`server/render`) applies the formatting rules for every feature, so no feature reads these settings itself.

## Requirements

### Requirement: Safe defaults and validation
Every formatting setting SHALL default to the plugin's original behaviour: compact feeds, comment excerpts, names
without mentions, every card field, three state buttons, Assign to me and Comment buttons, EU dates (17.11.2026)
and every digest section. An invalid value SHALL fall back to its default; a list setting (CardFields,
DigestSections) SHALL keep its known names in the given order and ignore unknown ones, and either case SHALL be
logged once when the configuration is saved.

#### Scenario: Unknown card field
- **WHEN** an admin sets CardFields to `due, colour, status`
- **THEN** cards show Due then Status, and the server log names `colour` as ignored

### Requirement: Date format
The DateFormat setting SHALL write dates as EU (`02.01.2006`), ISO (`2006-01-02`) or relative ("today",
"tomorrow", "yesterday", "in 2 days", "3 days ago") in the reader's language, falling back to the EU date beyond
14 days, in cards, task commands, the digest, reminders and feeds. Relative days SHALL be counted in the reader's
time zone, a bare due date being its own calendar day.

#### Scenario: Relative due date
- **WHEN** DateFormat is relative and a task is due the day after tomorrow
- **THEN** its card shows "in 2 days"

### Requirement: Bot name and icon
BotDisplayName SHALL rename the tix bot (its username stays tix) on activation and whenever the setting changes.
BotIconURL, when set, SHALL be fetched once when it changes, only over https, never from a loopback, private or
link-local address, at most 1 MiB and only as a PNG of at most 1024x1024 pixels, and SHALL become the bot's
profile image; the image SHALL be kept so a restart applies it again without fetching. A URL that fails these
checks SHALL be logged and leave the icon as it was; clearing the setting SHALL restore the tix icon.

#### Scenario: Icon on a private address
- **WHEN** an admin sets BotIconURL to `https://10.0.0.5/bot.png`
- **THEN** nothing is fetched from that address and the log says the URL points at a private address
