# Android transport: drive the Google Messages app over adb

The phone's own Messages app already knows, before anything is sent, whether a chat is encrypted. The Messages for web protocol does not expose that. This transport reads it off the real screen, the same way the iPhone setup drives Messages on a Mac, and it has no Google web session that can expire.

Turn it on with `RCS_TRANSPORT=android`. The default (`web`) is unchanged.

## How a send works

1. Make sure the phone is reachable over adb (reconnect if not), awake and unlocked.
2. Open the chat for the number (`am start -a SENDTO -d sms:<number>`), from a clean task so an old thread is never mistaken for this one.
3. **Know before sending.** Read the screen until the chat shows as end-to-end encrypted, or the settle budget runs out (default 60 s; a new chat reads "Text message" for up to a minute while the phone looks the contact up).
   - encrypted: continue
   - RCS without encryption (an iPhone on RCS stays here): refuse `e2ee_pending`, retryable
   - SMS after the whole budget: refuse `not_e2ee`
   - screen not readable: refuse `screen_unreadable` / `chat_not_open`
4. Type the message and read the box back. If it is not exactly what was asked, clear it and refuse `typing_mismatch`.
5. Last check before the tap, the authority: the send button's own label. It must read exactly **"Send encrypted message"** (real phone). Anything else ("Send SMS", plain RCS, an unknown label) clears the box and refuses, whatever the thread looked like. (Seen on a real screen: the button reads "Send SMS" in an SMS chat, and keeps that label after text is typed, when the hint is hidden.)
6. Tap send, then read the thread for our message and its status, and the protocol label the message itself carries (`You said <text> ., Now, SMS`).
7. Re-read the chat. The result carries `After` (encrypted / plain / SMS) and `MessageType` (4 RCS, 1 SMS). The existing service turns that into `SUCCESS_E2EE`, `SUCCESS_RCS`, `SUCCESS_SMS` (number blocked), `FAILED`, as before. A message whose own label says SMS counts as SMS even if the chat reads encrypted.

The send rule is the same as the web transport: only to a chat confirmed encrypted. There is no setting that relaxes it. A wrong selector can only make a chat read as "not encrypted", which refuses a send. It can never cause one.

One phone does one thing at a time, and sends are spaced like a person: at least `RCS_ANDROID_MIN_GAP_SECONDS` (45) plus up to `RCS_ANDROID_JITTER_SECONDS` (15) apart.

## Set up a phone

1. Android phone with a SIM, Google Messages as the default messaging app, chat features (RCS) on.
2. In Google Messages: Settings, RCS chats / chat features on. **Turn off "Automatically resend as text (SMS/MMS)"** (Settings, Advanced or RCS chats). Belt and braces; the screen checks above do not depend on it, but a fallback must never be possible. adb cannot toggle it, so it is a one-time manual step.
3. Developer options: USB debugging on. For a phone that lives next to a mini PC, plug it in (USB); the transport keeps the screen on while plugged in (`stay_on_while_plugged_in`).
   Wi-Fi instead of USB: `adb tcpip 5555`, then `adb connect <ip>:5555`; set `RCS_ANDROID_ADDR=<ip>:5555` so a dropped connection is re-made on its own.
4. Screen lock: none is simplest. With a numeric PIN set `RCS_ANDROID_PIN`. A phone that needs a PIN and has none configured is refused as `phone_locked`; nothing is guessed.
5. `adb` on the machine (Android platform-tools).

| Variable | Default | Meaning |
|---|---|---|
| `RCS_TRANSPORT` | `web` | `android` to use this transport |
| `RCS_ANDROID_ADB` | `adb` | adb binary |
| `RCS_ANDROID_SERIAL` | any | `adb -s` device (USB serial, or `host:port`) |
| `RCS_ANDROID_ADDR` | empty | Wi-Fi `host:port`, reconnected automatically |
| `RCS_ANDROID_PIN` | empty | numeric lock-screen PIN |
| `RCS_ANDROID_SELECTORS` | empty | JSON file overriding screen selectors (below) |
| `RCS_ANDROID_MIN_GAP_SECONDS` | 45 | minimum gap between sends |
| `RCS_ANDROID_JITTER_SECONDS` | 15 | extra random gap |
| `RCS_ANDROID_SETTLE_SECONDS` | 60 | how long a send waits for the chat to show encrypted |
| `RCS_ANDROID_INBOUND_SECONDS` | 60 | how often incoming replies are read (STOP opt-outs); 0 turns it off |

Sends can take minutes (settle, spacing, status), so the server's write timeout is raised for this transport and callers should allow about five minutes. The existing `rcsdirect` client already waits three.

## The probe tool

No server or database needed:

```
go run ./cmd/androidprobe health
go run ./cmd/androidprobe dump +15551234567     # the open chat's screen, node by node
go run ./cmd/androidprobe check +15551234567    # ENCRYPTED RCS / PLAIN RCS / SMS / UNREADABLE
go run ./cmd/androidprobe send +15551234567 "test message"
go run ./cmd/androidprobe inbound                # one pass: new incoming messages, and whether each is a STOP
```

`send` refuses everything the server would refuse.

## What is verified and what is not

Verified on a real Google Messages (Android 14 emulator, SMS only):

- opening a chat from `sms:<number>`, reading the message box and its hint ("Text message"), the send button ("Send SMS"), the per-message label (", SMS") and the thread bubbles; the `uiautomator dump` plumbing, including noise after the XML
- typing round-trips exactly: apostrophes, quotes, `$`, `&`, `;`, `%`, `#`, parentheses, blank lines, 280-character text
- a non-encrypted chat is refused and nothing is typed or sent
- recovery: adb server killed (3 of 3), screen off (3 of 3), numeric PIN lock (3 of 3), PIN not configured (refused `phone_locked`)
- the whole path through the service layer (results recorded, SMS number blocked), with a simulated phone for the RCS and encrypted cases
- incoming replies: a STOP sent from the emulator echoed back, was read off the list and thread, recorded as an opt-out in Postgres by the real server, and a later send to that number was refused `opted_out`

**Verified on a real RCS phone (Moto G 2025, Google Messages, 2026-11-13):**

- the empty-box hint of an RCS chat is "RCS message"; of an SMS chat, "Text message"
- the padlock is a node with description "Lock icon" in the thread-start banner, next to "RCS chat with <number>" and "This chat is now end-to-end encrypted. Learn more". It scrolls out of view on long threads, so it is only a hint
- an encrypted message's own label ends `, <status>. End-to-end encrypted message`; an incoming SMS ends `, Not end-to-end encrypted`
- with text typed, the send button's description is "Send encrypted message" (encrypted chat) or "Send SMS". This is the final gate
- status wording for our own messages: "Sending", then "Delivered", then "Read"
- live sends into three encrypted chats were delivered with the encrypted label; an iPhone on plain RCS was refused `e2ee_pending`; SMS-only numbers were refused `not_e2ee`, with nothing typed

If a different phone or a newer Messages version words any of this differently, change the selector, not the code. Do this once on a new phone:

1. `androidprobe check <a Google Messages contact>` after waiting a minute, then `androidprobe dump <number>`. Find the lock, the banner, and (with text typed) the send button in the dump.
2. If `check` does not say ENCRYPTED RCS for a chat that is encrypted, put the real strings in a JSON file (only the keys that differ) and set `RCS_ANDROID_SELECTORS`:
   ```json
   {"lock": "^lock icon$", "sendEncrypted": "^send encrypted message$", "hintRcs": "^rcs message$"}
   ```
   Keys: `package, composeId, sendId, sendDesc, sendEncrypted, encLabel, hintSms, hintRcs, banner, lock, statusSent, statusFailed, statusSending, bubbleId, messageContentId, msgSms, msgRcs, msgEncrypted, sendSms`. All patterns are case-insensitive regular expressions.
3. Check an iPhone number: it must read PLAIN RCS and be refused `e2ee_pending`. Check an SMS-only number: SMS, refused `not_e2ee`.
4. Send one test message to a Google Messages contact; it must come back `SUCCESS_E2EE`.
5. Leave it a day and run `androidprobe health` / `check` again.

## Known limits

- **Text:** printable ASCII and line breaks only (Android's `input text` cannot type emoji or accents). Anything else is refused `unsupported_text` before the phone is touched. Emoji needs an input method such as ADBKeyBoard; not built.
- **Inbound / STOP replies:** read off the screen. Every `RCS_ANDROID_INBOUND_SECONDS` the sender opens the Messages list, opens each chat whose last message or time changed, and passes the other side's messages to the same opt-out handler the web transport uses; a STOP blocks the number (`opted_out`). Limits: it can only see the chats and bubbles on screen (the newest ones), so it has to run often enough that fewer than a screenful of replies arrive between passes; a chat saved under a contact name (not a phone number) is skipped, because the number is not on the list, so keep outreach numbers unsaved; it shares the phone with sends, one thing at a time. Message ids are stable across restarts, so a repeat is harmless.
- **A send that cannot be confirmed:** if the message is in the thread but no final status shows, it is reported sent (`sent_unconfirmed`), not failed, so it is never retried into a duplicate.
- **Chat flips to SMS between the check and the tap:** caught by the send-button label check; if it still goes out as SMS the message label exposes it and the number is blocked.
- **Phone model differences:** the swipe that lifts the lock screen assumes a normal portrait phone. A phone with a different layout may need a different swipe.
- **Repeat sends of identical text (found on the Moto G, 2026-11-13, FIXED):** `androidprobe send` printed `sent=true` while the text was still in the compose box and nothing new was in the thread (confirmed by screenshot). Cause: the thread already held identical older outgoing messages (one marked "Read"), and the reader took the old bubble for the new send. A send now counts as registered only once the text has left the compose box; otherwise the result is `send_not_registered` and the box is cleared. Tests: `TestOldIdenticalMessageIsNotMistakenForOurSend`, `TestRepeatedMessageThatDoesSendIsReportedSent`. The tap itself missed because the phone was in landscape (full-screen keyboard, see above); the sender now locks portrait.
- **Several phones:** one Sender drives one phone. More numbers means more phones, each with its own `RCS_ANDROID_SERIAL` and process.

## Research notes (the questions in the brief)

- **adb + uiautomator vs uiautomator2/Appium:** built on plain adb and `uiautomator dump`, in Go, so there is no Python runtime, on-device server or Appium to keep alive on the box. Memory cost is a few adb processes, not a browser per phone. Appium/uiautomator2 would add a richer API at the price of that extra service.
- **Accessibility-service app on the phone:** would do the same with no computer, but needs a sideloaded app, an accessibility grant that Android can revoke, and can't be restarted remotely. The adb route reads the same screen, so the selectors carry over. Worth it only if a computer next to the phone is not acceptable.
- **Beeper / mautrix-gmessages session longevity:** the bridge's docs describe QR pairing as the normal method and the Google-account login as a fallback that needs cookies, and recommend a private window so the browser does not rotate them. No source found states how long a cookie login lasts. See [docs.mau.fi gmessages authentication](https://docs.mau.fi/bridges/go/gmessages/authentication.html). This is the problem the Android route avoids.
- **Is the compose hint reliable before sending:** on the screens tested, yes: the hint reads "Text message" for SMS, and a new chat starts there and changes once the phone's lookup lands, hence the settle loop. The RCS and encrypted wording is the part still to confirm on a real phone (above). Google's help pages also say a lock shows on the send button while composing and the banner reads "RCS chat with <contact>"; see [Check if a conversation is end-to-end encrypted](https://support.google.com/messages/answer/10252671). The lock selector is configurable for that reason.
- **Verified on a real phone (2026-11-12, Moto G 2025, Google Messages):** `androidprobe health` reports ready; `check` on a plain-SMS contact (an iPhone) reads `transport: SMS`, `e2ee: off`, hint "Text message", header "Texting with <number> (SMS/MMS)", no lock, no banner, and the sender refuses with `not_e2ee`. Also verified read-only on two existing end-to-end encrypted RCS chats on the same phone (`check` sends nothing): hint "RCS message"; thread line "RCS chat with <number>"; a node with description "Lock icon"; banner "This chat is now end-to-end encrypted. Learn more"; per-message label `You said <text> <date>, Read. End-to-end encrypted message`. `check` reads both as `ENCRYPTED RCS`, eligible to send. An unencrypted incoming SMS is labelled `<number> said <text> <time> ., Not end-to-end encrypted`. The default selectors match all of this with no change. Live sends into encrypted chats were also done (Kyle's and Amy's chats, 2026-11-13): both went out as encrypted messages (`End-to-end encrypted message` label, send button `Send encrypted message`). Findings from those runs:
  - **The "Lock icon" node is part of the thread-start banner, not the compose row.** It is on screen only while the top of the thread is visible (a short or new chat). A longer thread scrolls it away, so the lock cannot be the live signal. Before typing, a chat now also counts as worth trying when a message carries the app's own end-to-end-encrypted label (anchored to the end of its description, which message text cannot reach). The send button, which stays on screen, is the final gate.
  - **Landscape breaks the send tap.** Android's keyboard goes full screen with its own SEND button, so the tap on the app's send button misses and the text stays in the box. The sender now locks the phone to portrait itself.
  - **No internet means messages sit in "Sending" forever.** With Wi-Fi or mobile data not validated, RCS cannot register or send; with "Automatically resend as text" off the messages stay queued (never fall back to SMS). They send when data returns. Other phones then see this number as SMS-only (an incoming text from a friend's phone arrived as plain SMS for that reason). Check `dumpsys connectivity` for VALIDATED on the data network if sends stall.
  - Still unverified: the Sent / Delivered wording of a message we send (both test messages were still "Sending" because of the missing data connection).
- **Lock position (the press-report concern did not show up on this phone):** the lock was present in the uiautomator dump of real encrypted chats (see above), so the layout described by the 2026 reports was not in effect here. A different Google Messages version may differ.
- **Lock may have moved (unconfirmed, from 2026 press reports):** Google's help pages put the lock on the send button, but 9to5Google reports a layout test that moves the timestamp and the encryption lock behind a swipe left. If the lock is not in the uiautomator dump, the detector falls back to the "end-to-end encrypted" banner; with neither it returns `e2ee_pending` and refuses the send. That is a refusal, never a send. Do NOT widen `banner` to match "RCS chat with": that line appears on non-encrypted RCS chats (for example with an iPhone) and would read as encrypted.
- **"Automatically resend as text":** turn it off on the phone (setup step 2). adb cannot change it.
