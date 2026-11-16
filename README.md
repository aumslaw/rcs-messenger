# rcs-messenger

Sends RCS messages from an Android phone through a paired Google Messages for web session, using [libgm](https://github.com/mautrix/gmessages) from mautrix-gmessages. It refuses to send rather than fall back to SMS.

## Run

```
docker compose up -d
go run ./cmd/server
```

The server listens on `127.0.0.1:8080` and creates its two tables on startup.

| Variable | Default | Meaning |
|---|---|---|
| `ADDR` | `127.0.0.1:8080` | Listen address. The API has no auth, so keep it on localhost. |
| `DATABASE_URL` | `postgres://postgres:postgres@localhost:5432/rcs_messenger?sslmode=disable` | Postgres holding the paired session and the send results |
| `RCS_ENABLED` | `true` | Run the phone connection and allow sends |
| `RCS_DRY_RUN` | `false` | Accept sends without reaching the phone |
| `RCS_WAIT_SECONDS` | `20` | How long a send waits for a delivery status, capped at 20 |
| `RCS_MONITOR_SECONDS` | `60` | How often the health monitor checks the phone |
| `RCS_ALLOWLIST_PHONES` | empty | Comma-separated E.164 numbers; when set, only these can be sent to |
| `RCS_TRANSPORT` | `web` | `web` (Google Messages for web session) or `android` (drive the Messages app over adb). See below |
| `RCS_ANDROID_*` | see [docs/ANDROID.md](docs/ANDROID.md) | Serial, Wi-Fi address, PIN, selector overrides, send spacing, settle time, inbound polling |
| `DEBUG` | `false` | Debug logging |

A setting that is set but does not parse (for example `RCS_DRY_RUN=yes`) stops the server at startup. It never silently falls back to its default.

## The send rule

A message is sent only when end-to-end encryption is confirmed for that chat. There is no setting that relaxes this, and `RCS_REQUIRE_E2EE=false` stops the server from starting. If it cannot be confirmed, nothing is sent: no plain RCS, no SMS.

**Android transport** (the app's own screen is the authority):

| What the phone shows | Result |
|---|---|
| Hint "RCS message" and the chat shows encryption (the lock in the thread header, or a message the app labels end-to-end encrypted), and after typing the send button reads exactly "Send encrypted message" | Send |
| The send button reads anything else (plain RCS, "Send SMS", unknown) | Clear the box, refuse `e2ee_pending` |
| Hint "Text message" (SMS) | Refuse `not_e2ee` |
| RCS chat with no encryption shown (an iPhone on RCS) | Refuse `e2ee_pending` |
| Chat for a different number, no internet, phone locked, screen unreadable | Refuse |

**Web transport:**

| What the phone says | Result |
|---|---|
| Chat has an encrypted protocol tombstone **and** the capability record reads `yes` (the exact record observed on Google Messages contacts) | Send |
| Encrypted tombstone but capability `not_yet` (for example a contact who moved to an iPhone) | Refuse as `e2ee_pending`, retryable |
| Unfinalized first draft, whatever the capability says | Refuse as `e2ee_pending`: a capability `yes` is a prediction, not confirmation. An iPhone on plain RCS stays here, so it is never sent to |
| Capability `no`, or a chat recorded as RCS without encryption, or no tombstone in reach | Refuse as `not_e2ee` |
| Chat is SMS, SMS/MMS send mode, RCS off on the SIM, read-only, phone offline | Refuse |

After a send the chat is re-read as a second check. If the message went out unencrypted or as SMS anyway, it is logged at error level, recorded, and that number is never sent to again.

## Android transport (no Google web session)

`RCS_TRANSPORT=android` sends by driving the Google Messages app on an Android phone over adb. It reads the encrypted/plain/SMS answer off the screen before sending, and the send button's own label ("Send encrypted message") is the final gate at the moment of the tap. Nothing expires, nothing is paired. Setup, settings, what is verified and what still needs a real RCS phone: [docs/ANDROID.md](docs/ANDROID.md). Try it without the server: `go run ./cmd/androidprobe check +15551234567`.

## Pair a phone (web transport)

Open http://localhost:8080/internal/rcs/pair.

- **QR**: on the phone, Google Messages > profile menu > Device pairing > QR code scanner. Google has removed QR pairing in some countries.
- **Emoji**: POST a HAR captured on messages.google.com/web, signed in to the phone's Google account, to `/internal/rcs/pair/cookies`, then tap the emoji it returns on the phone.

The Google session behind emoji pairing expires within hours to a day. `/internal/rcs/pair/refresh` takes a fresh HAR without pairing again. Keeping that session alive is the open problem.

## Send

```
curl -X POST localhost:8080/internal/rcs/send \
  -H 'Content-Type: application/json' \
  -d '{"to":"+15551234567","message":"hi"}'
```

Every endpoint is documented in `bruno/`, including what each refusal reason means.

## Code

- `cmd/server`: config, wiring and routes
- `internal/providers/rcsandroid`: the Android transport (adb, screen reading, the send gate, incoming STOP replies)
- `cmd/androidprobe`: check, dump, send and inbound against a real phone, without the server
- `internal/providers/rcsphone`: the phone session, pairing, eligibility checks, sending, the contact capability lookup
- `internal/providers/rcsdirect`: the request and result types shared by the layers
- `internal/rcs`: the send policy, per-number send results, opt-out replies and the health monitor
- `internal/api/rcs`: HTTP handlers and the pairing page
- `internal/db`: Postgres connection and schema

```
go test ./...
```

Audit results and what changed: [AUDIT.md](AUDIT.md). Report: [docs/REPORT.md](docs/REPORT.md).

The opt-out and results store tests need a real Postgres and are skipped without one:

```
docker run -d --rm --name rcs-test-pg -e POSTGRES_PASSWORD=pg -e POSTGRES_DB=rcs_test -p 127.0.0.1:55432:5432 postgres:16
RCS_TEST_DATABASE_URL='postgres://postgres:pg@127.0.0.1:55432/rcs_test?sslmode=disable' go test ./internal/rcs
```
