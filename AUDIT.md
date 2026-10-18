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
