# Task cards and thread sync

With **Show task cards** on (System Console > Plugins > tix), a message that mentions a task such as
`infra-42` gets a card for it: up to three per message, matched against the project keys the author can see,
ignoring code and links. The card is coloured by the state's category and shows the title (linking to tix),
status, assignee, priority, due date, tags and the project; **Task card fields** picks which of these it shows
and in what order, **Date format** how the due date reads. Cards are channel posts, so their labels are in the
server's default language.

By default (**Task card style**: *Reply in the thread*) the message stays as written and the tix bot answers it
in its thread with the cards, so the message is never marked edited. *Inline on the message* attaches the cards
to the message itself, as earlier versions did. A task that already has a card in the thread, or got one in the
channel within the **Card quiet period** (10 minutes by default), is not carded again. Editing a message to add
a task cards it; deleting the message deletes its card.

A card is drawn with the author's own tix token, so a message only shows tasks its author can see; an author who
has not run `/tix connect` gets no cards. The buttons (up to three next states from the workflow, *Assign to me*
and *Comment*; **Task card state buttons** and the two **Show ... button** settings trim them) act with the token of whoever clicks them; a transition that needs a comment, and *Comment*, open a
dialog. Cards are redrawn in place after a button is used and when tix reports a change to the task.

**Thread sync.** A channel admin can run `/tix sync on` in a channel (`off` to stop, no argument to see the
state). Then a reply in a card's thread is added to the task in tix as a comment by whoever wrote it, with their
own token (people who have not connected get a one-time hint), and new tix comments on the task are posted into
the thread by the bot. Edits, bot and system posts, and the link the create dialog posts for you (*Post link in
thread*), are not mirrored, and a comment is never echoed back to the
thread or channel it came from. Everyone who can read the channel sees the mirrored comments.
