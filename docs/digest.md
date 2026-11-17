# Digest and reminders

Both are off until a connected person turns them on, and both come as DMs from the bot, read with that person's
own tix token, in their Mattermost language. Times follow the person's Mattermost time zone (automatic or
manual). A due date without a time of day (tix stores it as midnight UTC) is that whole calendar day wherever you
are: it is due today on that day and overdue only once the day is over in your time zone.

- `/tix digest on [HH:MM]` sends a daily summary at that time (default 09:00), Monday to Friday;
  `/tix digest days daily` includes weekends, `days mon-fri` goes back. It lists the open tasks assigned to you
  that are overdue, due today, newly assigned since the last digest, blocked or in progress (each task once, with
  a link and a `P1`/`P2` marker for high priorities), then the count per status. An admin can pick and reorder
the sections with **Digest sections**; a task then shows in the first listed section it fits. With nothing to report it says
  *All clear*; `/tix digest quiet on` skips those instead. `/tix digest now` sends it at once, `off` stops it and
  `status` shows the settings and the next run.
- `/tix reminders on` sends a DM in the morning (at your digest time, 09:00 without one) for each task assigned
  to you that is due that day, and one when a task becomes overdue (checked hourly between 08:00 and 21:00,
  for tasks due in the last seven days). Each reminder goes out once per task and due date; a new due date
  reminds again. On a digest day the morning reminders are part of the digest, not a second DM.

Reminders are off by default: they make the plugin call tix with your token on a schedule you did not start,
and they would arrive unannounced for everyone already connected when the plugin is upgraded.

These switches are independent of `/tix notifications`, which covers DMs about changes made to your tasks; turning
one off does not affect the other. Disconnecting (`/tix disconnect`) ends both.

One cluster-wide job checks every minute who is due. A digest that could not be sent within three hours of its
time (the server was down, tix kept failing) is skipped rather than sent late, and no one gets more than one
digest a day, across restarts and cluster nodes.
