# WhatsApp Assistant

Lets Claude read your WhatsApp so you can ask it things like *"what did I miss in
my work groups today?"* or *"summarise what the supplier said this week"*.

**It can only read. It cannot send messages, reply to anyone, or change
anything in WhatsApp.** That is built into the app itself, not a setting that
can be switched on.

> On Windows? Use [README-windows.md](README-windows.md) instead.

---

## What you need first

- A Mac with an **Apple chip** (see below)
- The **Claude** app installed and signed in — download it from
  [claude.ai/download](https://claude.ai/download) if you don't have it
- Your phone with WhatsApp on it, nearby

### Check your Mac has an Apple chip

Click the **** menu in the top-left corner → **About This Mac**, and look at
the top line:

- **Chip: Apple M1 / M2 / M3 / M4 …** → you're good, carry on.
- **Processor: … Intel …** → this version won't run on your Mac. Let me know
  and I'll send you a different build.

(Any Mac bought from late 2020 onwards has an Apple chip.)

---

## Installing (about two minutes)

**1. Double-click `WhatsApp-Assistant-macOS-AppleSilicon.mcpb`.**

Claude opens and asks whether you want to install the extension. Click
**Install**.

> If nothing happens, open Claude and go to **Settings → Extensions → Advanced
> settings → "Install Extension…"**, then pick the file. Dragging it into a
> chat does **not** work — that just attaches the file to the conversation.

> If macOS says the file is from an unidentified developer, right-click the file
> instead and choose **Open**, then confirm.

**2. Open Claude and start a new chat.** Type:

> check my whatsapp status

**3. Click "Always allow"** when Claude asks for permission to use the WhatsApp
tools. You only have to do this once.

**4. A square QR code appears on your screen.** On your phone:

- Open **WhatsApp**
- Go to **Settings → Linked Devices → Link a Device**
- Point your phone at the QR code on your Mac

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

**Your messages stay on your Mac.** They are copied into a private folder on
your own computer. They are not uploaded anywhere, and nobody else can see them.
The only thing that leaves your Mac is whatever Claude needs to answer the
specific question you asked — the same as if you had pasted it into a chat
yourself.

**About every three weeks, WhatsApp will unlink your Mac.** This is normal and
comes from WhatsApp, not from this app. When Claude tells you it can't see your
messages, just ask it to *check my whatsapp status* again and scan the new QR
code. It takes ten seconds.

**You do not have to leave the Mac on all day.** While it is off or asleep,
WhatsApp holds on to your messages and hands them over the next time the Mac is
awake and online. They keep the time they were originally sent, so *"what did I
miss today?"* still covers the whole day. Give it a couple of minutes after
waking — Claude checks whether it has caught up before summarising, and will say
so rather than hand you half a day and call it complete. If the Mac stays off
for more than about two weeks, WhatsApp unlinks it and you will need to scan a
new QR code.

**Voice notes are turned into text automatically.** You can read what someone
said, ask for a summary of it, and search voice notes by the words spoken in
them — just like typed messages. This happens entirely on your own Mac, so the
recordings are never uploaded anywhere.

The first time you install, your Mac quietly downloads the speech model it
needs (about 1.5 GB, once). Voice notes start becoming readable shortly after,
and older ones are worked through in the background.

---

## If something isn't working

Almost everything is fixed by asking Claude:

> check my whatsapp status

That command repairs the background service, tells you whether your account is
still linked, and shows you a fresh QR code if it isn't.

If you want to know whether Claude is seeing everything from today, ask:

> is my whatsapp up to date?

If Claude says it has no WhatsApp tools at all, go to **Settings → Extensions**
and switch WhatsApp Assistant **off and then on again**. Claude only reads the
list of tools when it starts, so that switch makes it look again.

If that does not help, quit the Claude app completely (**Claude → Quit**, not
just closing the window) and reopen it.

---

## Removing it

1. Double-click **`Uninstall WhatsApp Assistant.command`** and confirm. This
   stops the background service and removes the app's files, but **keeps your
   copied messages** so you can reinstall later without starting over.
   - If macOS refuses to open it, right-click the file and choose **Open**.
   - To delete the copied messages as well, drag the file into a Terminal
     window, type ` --purge` after it, and press Return.
2. In Claude, go to **Settings → Extensions** and remove **WhatsApp Assistant**.
3. Optionally, on your phone: **WhatsApp → Settings → Linked Devices**, and
   remove your Mac from the list.

---

## A note on how this works

This connects to WhatsApp the same way WhatsApp Web does, using an unofficial
method rather than a business account. It works well, but it is not an official
WhatsApp product and it is shared between friends rather than sold. If WhatsApp
ever objects to an account using it, the risk falls on that account — worth
knowing before you link a number your business depends on.
