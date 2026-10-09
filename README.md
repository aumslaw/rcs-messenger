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
| `RCS_REQUIRE_E2EE` | `false` | `true` only sends to chats already known to be end-to-end encrypted |
| `RCS_DRY_RUN` | `false` | Accept sends without reaching the phone |
| `RCS_WAIT_SECONDS` | `20` | How long a send waits for a delivery status, capped at 20 |
| `RCS_MONITOR_SECONDS` | `60` | How often the health monitor checks the phone |
| `RCS_ALLOWLIST_PHONES` | empty | Comma-separated E.164 numbers; when set, only these can be sent to |
| `DEBUG` | `false` | Debug logging |

## Pair a phone

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
- `internal/providers/rcsphone`: the phone session, pairing, eligibility checks, sending, the contact capability lookup
- `internal/providers/rcsdirect`: the request and result types shared by the layers
- `internal/rcs`: the send policy, per-number send results, opt-out replies and the health monitor
- `internal/api/rcs`: HTTP handlers and the pairing page
- `internal/db`: Postgres connection and schema

```
go test ./...
```
