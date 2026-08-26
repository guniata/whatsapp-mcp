# WhatsApp Assistant — Windows

Lets Claude read your WhatsApp so you can ask it things like *"what did I miss in
my work groups today?"* or *"summarise what the supplier said this week"*.

**It can only read. It cannot send messages, reply to anyone, or change
anything in WhatsApp.** That is built into the app itself, not a setting that
can be switched on.

> On a Mac? Use [README.md](README.md) instead.

---

## What you need first

- A **Windows 10 or 11** PC with a **64-bit** processor — almost every PC sold
  in the last decade. (If you ever see `win32` written down, that is just the
  name Claude uses for Windows in general; it does not mean 32-bit.)
- The **Claude** app installed and signed in — download it from
  [claude.ai/download](https://claude.ai/download) if you don't have it
- Your phone with WhatsApp on it, nearby

---

## Installing (about two minutes)

**1. In Claude, go to Settings → Extensions → Advanced settings →
"Install Extension…"** and pick the file you downloaded,
`WhatsApp-Assistant-Windows-x64.mcpb`. Click **Install**.

> Double-clicking the file in File Explorer usually works too. Dragging it
> into a chat does **not** — that just attaches the file to the conversation.

**2. Start a new chat and type:**

> check my whatsapp status

**3. Click "Always allow"** when Claude asks for permission to use the WhatsApp
tools. You only have to do this once.

**4. A square QR code appears on your screen.** On your phone:

- Open **WhatsApp**
- Go to **Settings → Linked Devices → Link a Device**
- Point your phone at the QR code on your screen

**5. Wait a minute, then ask Claude again:**

> check my whatsapp status

When it says your account is linked and shows a number of messages, you're done.
Your recent chat history is copying over in the background — give it a few
minutes to finish before expecting Claude to know everything.

---

## Things to try

- *"Summarise what I missed in my work groups today."*
- *"What's the latest with [person's name]?"*
- *"Did anyone ask me a question in the last two days that I haven't answered?"*
- *"Show me everything about the delivery from this week."*
- *"What did that voice message from this morning say?"*
- *"Every weekday at 8am, give me a summary of anything from yesterday that
  needs my attention."* — Claude can do this on a schedule, so a briefing is
  waiting for you each morning.

---

## Things worth knowing

**You do not have to leave the PC on all day.** While it is off, asleep, or
closed, WhatsApp holds on to your messages, and hands them over the next time
the PC is on and online. They keep the time they were originally sent, so
*"what did I miss today?"* still covers the whole day, not just the part where
the PC happened to be awake.

Two things follow from that:

- **Give it a couple of minutes after switching on.** Copying a day's messages
  across takes a little time, and voice notes take longer still because they
  have to be turned into text. Claude checks this for itself before it
  summarises anything, and will tell you if it is still catching up rather than
  hand you half a day and call it complete.
- **If the PC stays off for more than about two weeks,** WhatsApp unlinks it.
  Nothing is lost from what already copied across, but messages from the gap
  are gone and you will need to scan a new QR code. Ask Claude to *check my
  whatsapp status* and it will show you one.

**Your messages stay on your PC.** They are copied into a private folder on your
own computer. They are not uploaded anywhere, and nobody else can see them. The
only thing that leaves your PC is whatever Claude needs to answer the specific
question you asked — the same as if you had pasted it into a chat yourself.

**About every three weeks, WhatsApp will unlink your PC.** This is normal and
comes from WhatsApp, not from this app. When Claude tells you it can't see your
messages, just ask it to *check my whatsapp status* again and scan the new QR
code. It takes ten seconds.

**Voice notes are turned into text automatically.** You can read what someone
said, ask for a summary of it, and search voice notes by the words spoken in
them — just like typed messages. This happens entirely on your own PC, so the
recordings are never uploaded anywhere.

The first time you install, your PC quietly downloads the speech model it needs
(about 1.5 GB, once). Voice notes start becoming readable shortly after, and
older ones are worked through in the background.

---

## If something isn't working

Almost everything is fixed by asking Claude:

> check my whatsapp status

It now tells you which part is not working rather than just asking you to try
again, so paste what it says if you need help.

**"Starts at login: NO"** means Windows would not let it register the task that
starts the sync in the background. Everything still works while Claude is open —
it just will not collect messages when Claude is closed. To fix it, close Claude,
right-click the Claude icon, choose **Run as administrator**, and ask *check my
whatsapp status* once. After that you can go back to opening Claude normally.

That command repairs the background service, tells you whether your account is
still linked, and shows you a fresh QR code if it isn't.

If you want to know whether Claude is seeing everything from today, ask:

> is my whatsapp up to date?

If Claude says it has no WhatsApp tools at all, go to **Settings → Extensions**
and switch WhatsApp Assistant **off and then on again**. Claude only reads the
list of tools when it starts, so that switch makes it look again.

If that does not help, quit the Claude app completely (right-click its icon in
the system tray → **Quit**, not just closing the window) and reopen it.

---

## Removing it

1. Double-click **`Uninstall WhatsApp Assistant.bat`** and confirm. This stops
   the background service and removes the app's files, but **keeps your copied
   messages** so you can reinstall later without starting over.
   - To delete the copied messages as well, open the folder in File Explorer,
     type `cmd` in the address bar and press Return, then run:
     `"Uninstall WhatsApp Assistant.bat" --purge`
2. In Claude, go to **Settings → Extensions** and remove **WhatsApp Assistant**.
3. Optionally, on your phone: **WhatsApp → Settings → Linked Devices**, and
   remove your PC from the list.

---

## A note on how this works

This connects to WhatsApp the same way WhatsApp Web does, using an unofficial
method rather than a business account. It works well, but it is not an official
WhatsApp product and it is shared between friends rather than sold. If WhatsApp
ever objects to an account using it, the risk falls on that account — worth
knowing before you link a number your business depends on.
