# Audit

Rule: send only when encrypted RCS is confirmed. Otherwise refuse. Never fall back to plain RCS or SMS.

## What was wrong

1. Encryption was optional (`RCS_REQUIRE_E2EE` defaulted to false).
2. The wait loop gave up after 15s and sent anyway. An iPhone on plain RCS never confirms, so it always got plain RCS.
3. A new chat whose capability read "no" could still be sent.
4. If the capability lookup failed, it sent.
5. `/check` and `/send` used different rules.
6. STOP replies were logged but never blocked sending.

## What changed

- One rule in `eligibility()`: send only if the chat has an encrypted tombstone, or it is a new chat and the capability reads `yes`. Everything else is refused.
- Running out of wait time now refuses instead of sending.
- `RCS_REQUIRE_E2EE` is removed. The server won't start if it is set to false.
- A message that still goes out unencrypted or as SMS is logged as an error and that number is blocked.
- STOP replies now block the number (`opted_out`).
- Tests rewritten for the new rule, plus new ones for the wait loop and opt-outs.

## Not changed

- A send that times out waiting for status can be retried and sent twice.
- The Google session expires.
- If the phone falls back to SMS after the check, we can only detect it afterwards.

## Test on a real phone

1. `capability = yes` means the first message goes out encrypted.
2. An iPhone reads `not_yet` and is refused (an iOS 26.5 iPhone may legitimately become encrypted).
3. An SMS-only number is refused.
4. Turn the recipient's data off mid-send: it must fail or be flagged, never go out as SMS silently.
5. Fresh Google Messages contacts settle to `yes` within 15s. If not, raise `settleBudget`.

---

# Round 2 audit (2026-11-13): whole send path, both transports

Four independent read-only reviews (service and API, web protocol, Android adb transport, reliability). Every
finding below was checked against the code; the fixed ones have a test that fails on the old code.

## Fixed

| Severity | Finding | Fix | Test |
|---|---|---|---|
| CRITICAL | Android lock/banner patterns matched message text. A customer saying "is this end-to-end encrypted?", or any "End-to-end encrypted message" label, made a plain RCS chat read as encrypted. | Lock is a dedicated node matched by an anchored description ("Lock icon"); message bubbles and labels are never searched; the banner alone is never enough. | `TestMessageTextCannotMakeAPlainRCSChatLookEncrypted`, `TestRealShapedEncryptedChatReadsEncrypted` |
| CRITICAL | A leftover draft hid the compose hint, and the banner alone then read as encrypted. | A box with no readable hint reads unknown; nothing is guessed from the thread. | `TestLeftoverDraftWithBannerIsNotEncrypted` |
| HIGH | Android: an older identical message in the thread was taken for the new send ("sent" with the text still in the box). | A send counts only once the text has left the compose box. | `TestOldIdenticalMessageIsNotMistakenForOurSend` |
| HIGH | Android: message text could set the status ("Payment failed" read as a failed send, so callers retried). | Bubbles and labels are excluded from the status search. | `TestMessageTextCannotSetTheSendStatus` |
| HIGH | Android: the chat was not re-checked between settle and typing. | Eligibility is re-run on the read taken right before typing. | `TestChatThatDropsToPlainRCSAfterSettlingIsRefused` |
| HIGH | Android: a caller disconnect after the tap returned a bare error: recorded as "nothing went out", no block, retry duplicates. | After the tap the send runs on an uncancelable context and always returns a result. | `TestCallerCancelAfterTheTapStillReturnsAResult` |
| HIGH | Service: an outcome could fail to be recorded when the caller was gone, so an SMS fallback never blocked the number. | `record` writes on `context.WithoutCancel` with its own timeout. | `TestOutcomeIsRecordedEvenWhenTheCallerWentAway` |
| HIGH | Android: a STOP whose opt-out failed to save was marked delivered and never retried. | The inbound handler returns an error; a message is marked delivered only after success, and a failed chat is re-read next pass. | `TestFailedInboundHandlerIsRetriedNextPass` |
| HIGH | Web: capability `yes` was decided on Flag1 alone, so a non-Google-Messages contact could read as encrypted and be sent plain RCS. | `yes` only for the observed record `{1, 1, 0x0201}`. | `TestOnlyTheObservedGoogleMessagesRecordIsYes` |
| HIGH | Web: an old encrypted tombstone was trusted without the current capability (contact moved to an iPhone). | A tombstone must be backed by capability `yes`; `no` refuses, anything else is `e2ee_pending`. | `TestEligibilityRequiresConfirmedEncryption` |
| HIGH | Web: the phone accepted a send but reported no final status in the wait, and the result said "not sent": recorded FAILED, retried into a duplicate, an SMS fall back never flagged. Found by the simulation. | Result is sent with outcome unknown, and the chat is re-read after the send. | `TestSimNoFinalStatusWithinTheWaitIsSentOutcomeUnknown` |
| MEDIUM | A mistyped setting silently fell back to its default (`RCS_DRY_RUN=yes` became false, so real sends). | Unparseable settings stop the server at startup. | `TestSettingsThatDoNotParseAreErrorsNotDefaults` |

## Fixed in the final pass

| Sev | Problem | Fix | Test |
|---|---|---|---|
| HIGH | Web: a send request that errored after it left (timeout, dropped connection) was reported as an error, so a caller retried into a duplicate. | The thread is re-read for the send's id. Found or unreadable: sent, outcome unknown, and the chat is re-checked. Only a readable thread without the message counts as not sent. | `TestSimSendRequestErrorThatLandedIsSentOutcomeUnknown`, `...UnreadableThreadIsSentOutcomeUnknown`, `...DidNotLandIsAnErrorNotASend` |
| HIGH | Web: a lost session (unpaired, fatal connection error, disconnected, logged out) did not stop a send. | `web_session_lost` refusal before anything else; nothing reaches the client. | `TestSimLostWebSessionRefusesWithoutSending` |
| MEDIUM | `SUCCESS_UNVERIFIED` did not block the number. | It blocks like the SMS and unencrypted outcomes. | `TestClassify` (blocks list) |
| n/a | "`Reload` can race the reconcile loop" was stale: both already take `connMu`. | No change needed. | |

## Fixed after that (resolve-all pass)

| Sev | Problem | Fix | Test |
|---|---|---|---|
| HIGH | A caller retrying a send (timeout, hang-up) could start a second send; the HTTP write timeout (30 s) was shorter than a send. | `Idempotency-Key` header or `idempotencyKey` field: the same key never sends twice, in flight or after; bound to the number (422 on another). Sends run detached from the request. Write timeout 4 min. | `TestIdempotent*`, `TestSendRouteIdempotencyKeyReplaysAndBindsNumber` |
| HIGH | Android: split-screen broke the "which side is ours" test. | `wm size` vs the window bounds; not full screen refuses `window_not_fullscreen` before typing. | `TestSplitScreenWindowIsRefusedWithNothingTyped` |
| HIGH | Android: a STOP in a chat saved under a contact name was skipped. | The number is read from the chat title / details page; unresolved chats are recorded and the sender refuses them (`unresolved_chat`). | `TestStopInContactNamedChatOptsTheNumberOut`, `TestUnresolvedContactChatIsRecordedAndSendRefuses` |
| MEDIUM | Android: identical text scrolling off made message ids repeat. | The id includes the number, the text, the bubble above and an occurrence count. | `TestIdenticalMessagesGetDistinctStableIDs` |
| MEDIUM | Logs could contain phone numbers; endpoints had no auth. | Every log line is scrubbed of phone-number runs, fields are masked to the last two digits. `RCS_API_TOKEN` (bearer) guards every route but `/health`; a non-loopback `ADDR` without it refuses to start. | `TestLogLinesNeverCarryAFullNumber`, `TestTokenGuardsEveryRouteButHealth`, `TestOpenBindNeedsAToken` |

## Remaining limits (not code defects)

- **The web path has not run live** (pairing needs a phone camera): the wire format and Google login are unproven.
- **Idempotency keys live in memory**: they do not survive a restart (the Results store has no table for them).
- **Android, unverified on a real phone:** the contact-details navigation and `wm size` parsing are written against fakes. The top bubble on screen has no context above it, so a new identical message landing exactly there can be missed.
- Pair page cannot be used from a browser once `RCS_API_TOKEN` is set (it needs the bearer header).

## Added after live runs (same day)

- **The send button is the final gate** (`Send encrypted message`, real phone). The thread-start "Lock icon" scrolls away on long threads, so it is only a pre-typing hint; see docs/ANDROID.md. Test: `TestSendButtonThatIsNotEncryptedIsTheFinalGate`, `TestLongThreadIsRecognisedByTheAppsMessageLabel`.
- **Portrait is enforced by the sender** (`KeepAwake`): in landscape the keyboard goes full screen and the send tap misses.
- **Root cause of "friend's text arrives as SMS" on the test phone:** the phone had no validated data connection, so RCS could not register.

## Web path: simulated, not run live

`internal/providers/rcsphone/sim_test.go` runs `Agent.Send` against a scripted fake Google Messages client (`fake_test.go`, behind the small `gmClient` interface): every refusal case asserts `SendMessage` was never called, plus the send, failure, cancelled-caller, serialised-send and fresh-chat-becomes-encrypted cases. Breaking the rules on purpose (fresh chat on capability alone, old marker without capability, XMS allowed, no send lock, unknown outcome as not-sent) makes those tests fail. Not proven: that the real phone replies in these shapes, and the libgm wire and auth. That needs a paired session.

## Still to prove on the phone

The live send into an encrypted chat and the Sent/Delivered wording of a message we send. Typed-text state: whether the padlock node is still present while text is in the box (the code does not rely on it).
