# Report: Audit and fix of the Google Messages / RCS automation

## The goal

The bounty asked for four things:

1. Audit the existing Google Messages automation and RCS infrastructure.
2. Identify and fix the bugs that cause messages to be sent without end-to-end encryption (E2EE).
3. Make sure messages are only sent when E2EE is confirmed, with no fallback to unencrypted RCS or SMS.
4. Refactor and clean up the implementation for reliability and stability.

The rule behind all four: a text goes out only if the chat is confirmed end-to-end encrypted. If it cannot be confirmed, nothing is sent.

## Result

All four are done and tested on a real Android phone running Google Messages, with the limits listed at the end. 200 automated tests pass (3 more need a Postgres database and are skipped without one). Build, vet and the full suite pass with the race detector.

## What I did

I ran four independent read-only reviews, one per area: the service and API layer, the web-protocol (libgm) path, the Android transport, and general reliability. I checked every finding against the code, fixed the ones that could send without E2EE or misreport a send, and wrote a test for each fix. I confirmed that each new test fails on the old code. Everything is written up in `AUDIT.md`.

## What was wrong, and what changed

| Severity | Problem | Fix |
|---|---|---|
| Critical | Android: the lock and banner checks matched message text. A customer replying "is this end-to-end encrypted?", or any old "End-to-end encrypted message" label, made a plain RCS chat read as encrypted. | The lock must be a dedicated node matched by an anchored description. Message bubbles and labels are never searched. The banner alone is never enough. |
| Critical | Android: a draft left in the message box hid the compose hint, and the banner alone then counted as encrypted. | A box with no readable hint reads "unknown". Nothing is guessed from the thread. |
| High | Android: no final check at the moment of sending. | After typing, the app's own send button must read exactly "Send encrypted message". Anything else clears the box and refuses. This is the final gate. |
| High | Android: an older identical message in the thread was taken for the new send, so an unsent message was reported as sent. | A send counts only once the text has left the compose box. |
| High | Android: message text could set the delivery status ("Payment failed" read as a failed send, so callers retried). | Message bubbles and labels are excluded from the status search. |
| High | Android: the chat was not re-checked between the first read and typing. | Eligibility is re-run on the read taken right before typing. |
| High | Android: the open chat's number was never checked. | The title bar must show the target number, or nothing is typed. |
| High | A caller disconnecting after the tap lost the outcome. A number that fell back to SMS was never blocked, and a retry could duplicate the send. | After the tap the send runs to completion and always returns a result. Outcomes are recorded even when the caller has gone. |
| High | A STOP reply whose opt-out failed to save was marked delivered and never retried. | It is retried on the next pass. "Please stop" and "stop texting me" now count as STOP; sentences that merely contain the word do not. |
| High | Web path: the capability check said "yes" on one flag alone, so a contact on carrier RCS without Google Messages could read as encrypted. | "Yes" only for the one record shape actually observed on Google Messages contacts. |
| High | Web path: an old encrypted marker was trusted on its own, for example after a contact moved to an iPhone. | The marker must be backed by a current capability of "yes". |
| High | Web path: a brand-new chat was sent on a prediction, before any encrypted marker existed. | Strict: a new chat is refused until the marker exists. |
| High | Web path: a lost session did not stop a send, and a send request that errored after it left could be retried into a duplicate. | `web_session_lost` refusal; the thread is re-read for the send id and a possible send is reported as sent, outcome unknown. |
| Medium | `SUCCESS_UNVERIFIED` did not block the number. | It blocks like the other non-encrypted outcomes. |
| High | Web path: if the phone accepted a send but reported no final status within the wait, the result said "not sent". The caller recorded FAILED and could retry into a duplicate, and a fall back to SMS went unflagged. Found by the simulation. | The result now says sent, outcome unknown, and the chat is re-read after the send so an unencrypted or SMS outcome is flagged and the number blocked. |
| Medium | A mistyped setting silently used its default (`RCS_DRY_RUN=yes` became false, so real sends). | Settings that do not parse stop the server at startup. |
| Medium | Two requests for one number could both pass the block checks before either recorded. | Sends to one number run one at a time. |
| Medium | Reliability: a connect race, Health reporting "ready" while busy, phone numbers and the PIN in adb error text, no internet check. | Fixed. A phone with no working internet is refused, because messages would sit queued forever. The sender locks the phone to portrait, because in landscape the keyboard covers the send button. |

## What it does now

- **Encrypted chat:** types the message, confirms the send button says "Send encrypted message", taps send, then reads the thread for the status and the "End-to-end encrypted message" label.
- **Plain RCS (for example an iPhone):** refused as `e2ee_pending`. Nothing is sent.
- **SMS-only number:** refused as `not_e2ee`. Nothing is typed.
- **Anything unclear** (wrong chat, no internet, locked phone, unreadable screen): refused.
- **STOP replies** are read off the screen and block the number.
- A message that still goes out unencrypted or as SMS anyway is logged as an error, recorded, and the number is never sent to again.

## Evidence from a real phone

Moto G (2025), Google Messages, Android:

- Sends into three existing encrypted chats were delivered, each with the "End-to-end encrypted message" label and the status moving from Sending to Delivered to Read.
- An iPhone on RCS was refused `e2ee_pending` with nothing typed.
- SMS-only numbers were refused `not_e2ee` with nothing typed.
- The real wording for the lock, the banner, the message labels and the send button is recorded in `docs/ANDROID.md`.

## How to verify

```
go build ./... && go vet ./... && go test -count=1 -race ./...
go run ./cmd/androidprobe health
go run ./cmd/androidprobe check +15551234567    # read only, sends nothing
```

## Known limits and open items

- **The web-protocol path was simulated, not run live.** The whole send path (eligibility, settle wait, status matching, after-send re-read) runs against a scripted fake of the Google Messages client in `internal/providers/rcsphone/sim_test.go`: 13 refusal cases, each asserting that nothing was sent, plus sends, failures, a cancelled caller, serialised sends and a fresh chat that becomes encrypted. Breaking the key rules on purpose makes those tests fail. What the simulation cannot prove is that the real phone replies in the shapes the fake uses (the capability record shape comes from earlier real observations). That needs a paired Google session. The behavior change there is the strict rule for brand-new chats, so please review that one.
- **"Automatically resend as text" must be turned off by hand on each phone.** The code can neither read nor change that setting. If it is on and the recipient's data drops after the tap, the phone could send the message as SMS. The number would then be flagged and blocked, but that message cannot be recalled.
- Lower-priority items, each described in `AUDIT.md`: the HTTP write timeout is shorter than a long send (needs a caller idempotency key), inbound STOP is skipped for chats saved under a contact name, and message ids repeat when identical text scrolls off screen.
- Text is printable ASCII and line breaks only (Android's `input text` cannot type emoji or accents).

## Files

- `AUDIT.md`: the audit, every finding and its status.
- `docs/ANDROID.md`: Android transport setup, verified screen wording, limits.
- `README.md`: the send rule for both transports and the settings.
