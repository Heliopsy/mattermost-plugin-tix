# digest Specification

## Purpose

Give each connected person who asks for it a daily DM summary of the tix tasks assigned to them and a DM when one
of those tasks falls due or becomes overdue, read with that person's own tix token and scheduled by one
cluster-wide job.

## Requirements

### Requirement: Opt-in personal settings

The plugin SHALL keep the digest and the due reminders off for every person until that person turns them on with
`/tix digest on [HH:MM]` or `/tix reminders on`. It SHALL refuse to turn either on for a person who has not run
`/tix connect`, SHALL keep the settings per Mattermost user, and SHALL treat them as independent of
`/tix notifications`.

#### Scenario: Default off

- **WHEN** a person has connected to tix and never ran `/tix digest` or `/tix reminders`
- **THEN** the plugin sends them no digest and no reminder

#### Scenario: Not connected

- **WHEN** a person who has not run `/tix connect` runs `/tix reminders on`
- **THEN** the plugin answers that they must connect first and saves nothing

#### Scenario: Notifications off

- **WHEN** a person with the digest on has run `/tix notifications off`
- **THEN** the digest is still sent

### Requirement: Digest schedule

The plugin SHALL send the digest at the person's chosen local time (09:00 when none is given) in their Mattermost
time zone, automatic or manual, on Monday to Friday unless they chose `/tix digest days daily`. It SHALL send at
most one digest per person per local day, across plugin restarts and cluster nodes, and SHALL skip a digest that
could not be sent within three hours of its time instead of sending it late.

#### Scenario: On time in the person's zone

- **WHEN** a person in Europe/Sofia chose 08:30 and the clock reaches 06:30 UTC on a Tuesday in winter
- **THEN** the plugin sends their digest

#### Scenario: Daylight saving change

- **WHEN** the person's zone moves its clocks between two digest days
- **THEN** the digest still arrives at the chosen wall-clock time on both days

#### Scenario: Weekend

- **WHEN** it is Saturday and the person kept the default days
- **THEN** no digest is sent

#### Scenario: Restart after sending

- **WHEN** the plugin restarts after a digest went out and the job runs again the same day
- **THEN** no second digest is sent that day

#### Scenario: Missed by hours

- **WHEN** the job first runs more than three hours after the person's digest time
- **THEN** that day's digest is skipped and the next one is scheduled

### Requirement: Digest content

The digest SHALL list, as links to tix with the reference, title and a marker for priorities 1 and 2, the open
tasks assigned to the person that are overdue, due today, newly assigned since the previous digest, blocked or in
progress, each task in the first of those sections that applies, followed by the number of open tasks per status.
The DigestSections setting SHALL choose the sections and their order (overdue, due_today, new, blocked,
in_progress, counts; all by default); a task SHALL be listed in the first listed section it fits, so leaving a
section out moves its tasks to the next listed one they fit. Unknown section names SHALL be ignored with a log
line. The digest SHALL be written in the person's Mattermost language, with dates in the DateFormat setting and
their time zone.
Tasks in a terminal, done or cancelled state SHALL be left out. With nothing in any section the digest SHALL read
"All clear", unless the person chose `/tix digest quiet on`, in which case it SHALL NOT be sent.

#### Scenario: Overdue and due today

- **WHEN** one assigned task's due time has passed and another falls due before local midnight
- **THEN** the first is listed under Overdue and the second under Due today

#### Scenario: Bare due date

- **WHEN** a task's due date has no time of day (tix stores it as midnight UTC) and it is that day in the
  person's time zone
- **THEN** it is listed under Due today, not Overdue, until that day is over in their time zone

#### Scenario: Sections reordered

- **WHEN** DigestSections is `in_progress, overdue`
- **THEN** the digest lists In progress first, then Overdue for the overdue tasks that are not in progress, and no
  per-status counts

#### Scenario: Newly assigned

- **WHEN** a task was assigned to the person after their previous digest
- **THEN** it is listed under Newly assigned, and nothing is listed there in a person's first digest

#### Scenario: Quiet empty digest

- **WHEN** nothing is overdue, due today, new, blocked or in progress and quiet is on
- **THEN** no digest is sent

### Requirement: Due reminders

With reminders on, the plugin SHALL DM the person once on the morning of a due day about each assigned open task
due that day, and once when an assigned open task becomes overdue (a bare due date once its day is over in the
person's time zone, a due time once it has passed), checking between 08:00 and 21:00 local time
for tasks due within the last seven days. It SHALL NOT send the same reminder twice for the same task, kind and
due date, recording each sent reminder in a KV record that outlives the window in which it could be sent again.
On a day the person gets a digest, the morning reminders SHALL be part of the digest instead of a separate DM.

#### Scenario: Due today

- **WHEN** a task assigned to the person is due at 15:00 today and the morning run comes
- **THEN** the person gets one DM naming it as due today

#### Scenario: Becomes overdue

- **WHEN** that task's due time passes and the next hourly check runs
- **THEN** the person gets one DM naming it as overdue, and later checks send nothing more about it

#### Scenario: Due date moved

- **WHEN** a task the person was reminded about gets a new due date
- **THEN** the new date is reminded once again

#### Scenario: Folded into the digest

- **WHEN** a person with digest and reminders on has an overdue task at digest time
- **THEN** the task appears in the digest and no separate reminder is sent for it

### Requirement: Bounded background job

The plugin SHALL run one scheduled job across the cluster (`cluster.Schedule`) once a minute, handling at most a
bounded number of people per run with a few in parallel, so that one slow tix answer delays nobody else. It SHALL
save each person's next run before sending anything, retry after five minutes when tix fails, skip the run when
tix rejects the person's token, drop the settings of a person who is no longer connected, and stop the job when
the plugin is deactivated.

#### Scenario: tix fails transiently

- **WHEN** tix answers an error at digest time
- **THEN** nothing is sent and the digest is retried five minutes later

#### Scenario: Disconnected person

- **WHEN** the job reaches a person who has run `/tix disconnect`
- **THEN** their digest and reminder settings are removed
