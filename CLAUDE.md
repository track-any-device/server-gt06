# server-gt06 — AI Instructions

This is the **GT06/Concox TCP server** for the Track Any Device platform.
Language: Go 1.23 | Docker image: `trackanydevice/server-gt06`

This server accepts long-lived TCP connections from GT06-compatible GPS devices (Concox GT06N/D,
Mictrack MT600, WeTrack2, and any third-party tracker using the GT06 binary protocol), decodes
binary frames, and publishes normalised telemetry to a Redis Stream consumed by the Laravel queue
worker (`package-gt06`'s `gt06:consume` command). Outbound commands arrive via Redis pub/sub.

Read this file before making any change.

---

## Platform-Wide Rules

These three rules apply in every repository under the `track-any-device` organisation.

**Cross-repo changes: file a GitHub issue first.**
If a task in this repository requires a change in another package or server app — stop. Open a
GitHub issue in the target repository describing exactly what is needed and why. Reference that
issue number in your commit message (`ref track-any-device/{repo}#{n}`). Do not directly edit
files in another repository.

The Redis Stream key (`gt06:telemetry`) and command channel pattern (`gt06:cmd:{imei}`) are
**shared contracts** with `package-gt06`. Any rename must be coordinated via a cross-repo issue
filed against `package-gt06` before merging here.

**Release order: packages before server apps.**
This is a Go server — it does not consume PHP packages. However, if a change here requires a
corresponding change in `package-gt06`, release `package-gt06` first (after `package-core`
if core also changed), then deploy this server.

**Database layer lives in `package-core` only.**
This server reads `devices` for device approval lookup only. Any schema change to that table must
be initiated via an issue against `package-core` to add the migration there first.

---

## Rule 1 — Plan before implementing

Before writing any code, ask clarifying questions. Present a plan and get explicit agreement.
Only begin once the approach is confirmed.

---

## Architecture

```
Device (TCP :7019)
  → per-device goroutine
  → frame decoder (GT06 binary: 0x78 0x78 short / 0x79 0x79 long, CRC16-IBM)
  → Redis Stream XADD gt06:telemetry {event, imei, payload_json, published_at}

Outbound:
  Redis SUBSCRIBE gt06:cmd:{imei}
  → frame encoder → TCP socket write
```

One goroutine per connected device. Session state (IMEI, login status) is stored in Redis under
`gt06:session:{imei}` with a 24-hour TTL, reset on each heartbeat. Online presence is tracked in
the `gt06:online` sorted set (score = last-heartbeat nanosecond timestamp).

---

## GT06 Frame Wire Format

### Short packet (body < 256 bytes — all common messages)
```
0x78 0x78 | Len(1) | Protocol(1) | Body(N) | Serial(2) | CRC16-IBM(2) | 0x0D 0x0A
```

### Long packet (body ≥ 256 bytes — batch uploads)
```
0x79 0x79 | Len(2) | Protocol(1) | Body(N) | Serial(2) | CRC16-IBM(2) | 0x0D 0x0A
```

**CRC16-IBM** covers bytes from `Len` through `Serial` (inclusive).
Polynomial: `0x8005`, initial value: `0x0000`, input reflected, output reflected.

Start bytes distinguish packet type: `0x78 0x78` = short, `0x79 0x79` = long.
End sentinel: `0x0D 0x0A` (CRLF). The reader must consume exactly `Len` body bytes
between the protocol byte and the serial number.

---

## Protocol Numbers (Message Types)

| Code | Name | Handler action |
|---|---|---|
| `0x01` | Login (IMEI) | CheckOrCreate → ACK or reject+close |
| `0x10` | GPS Location | Decode → publish `location` event |
| `0x11` | Status (battery/signal) | Decode → publish `status` event |
| `0x12` | Online command response | Log device ACK (no stream publish) |
| `0x13` | Heartbeat | Refresh Redis TTL → ACK |
| `0x14` | GPS+LBS query response | Decode → publish `location` event |
| `0x15` | UTC time calibration | ACK only, no stream publish |
| `0x16` | LBS alarm | Decode → publish `alarm` event |
| `0x17` | Battery level | Decode → publish `status` event |
| `0x19` | GPS+LBS+Status combo | Decode all fields → publish `location` |
| `0x1A` | GPS+LBS data | Decode → publish `location` event |
| `0x22` | GPS+Network+LBS | Decode → publish `location` event |
| `0x25` | Batch locations | Iterate entries → publish each as `location` |
| `0x26` | LBS+Alarm | Decode → publish `alarm` event |
| `0x27` | WiFi information | Log only — no GPS coordinates, no stream publish |
| `0x28` | Speed alarm | Decode → publish `alarm` with `overspeed` flag |
| `0x2A` | GPS+LBS extended | Decode → publish `location` event |

Unknown protocol numbers: log with hex code, send no response, increment `gt06_unknown_messages_total`.

---

## Rule 2 — Never drop a device connection silently

When a device disconnects, the goroutine must:
1. Delete `gt06:session:{imei}` from Redis
2. Remove the IMEI from `gt06:online` sorted set
3. Decrement the `gt06_connections_active` Prometheus gauge
4. Log the disconnect with IMEI, remote address, and reason

Silent goroutine exits cause phantom session entries and mislead the offline-detection logic in
`package-gt06`. The `defer reg.Unregister(ctx, sess)` pattern (same as JT808) must always run.

---

## Rule 3 — CRC must be validated on every inbound frame

All inbound frames must have their CRC16-IBM checksum validated before dispatch. Frames with
invalid CRC must be discarded and `gt06_decode_errors_total` incremented. Never process a
corrupted frame. Log the bad frame's IMEI (if known), remote address, and computed vs expected
checksum.

---

## Device Lifecycle

```
1. TCP connect — goroutine spawned, auth timer started (30 s)
2. 0x01 Login (IMEI in body) → MySQL CheckOrCreate → ACK (0x01) or reject+close
3. Session → StateLoggedIn; subsequent messages drop if still StateConnected
4. 0x10 / 0x1A / 0x19 / 0x22 … GPS reports (ongoing) → stream publish → ACK
5. 0x13 Heartbeat → Redis TTL refresh → ACK
6. Idle 3 min / TCP close → goroutine cleanup via defer
```

GT06 has **no separate auth-token step** (unlike JT808's 0x0102). A successful Login with an
approved IMEI moves the session directly to `StateLoggedIn`. All non-login messages received
before `StateLoggedIn` are discarded with a debug log.

Unapproved devices (not in MySQL `devices` table or `is_approved = false`): auto-insert as
`status=pending`, respond with login failure code, close the connection after 200 ms grace.
`package-gt06` receives no stream events for unapproved devices.

---

## Server Response Frames

| Trigger | Response bytes |
|---|---|
| Login success (0x01) | `0x78 0x78 0x05 0x01 {serial:2} {crc:2} 0x0D 0x0A` |
| Heartbeat (0x13) | `0x78 0x78 0x05 0x13 {serial:2} {crc:2} 0x0D 0x0A` |
| Location ACK (0x10) | `0x78 0x78 0x05 0x10 {serial:2} {crc:2} 0x0D 0x0A` |
| Outbound command (0x80) | Full command frame built by the command encoder |

The response serial mirrors the device's serial number from the inbound frame.

---

## Prometheus Metrics (`:9091/metrics`)

| Metric | Type | Description |
|---|---|---|
| `gt06_connections_total` | Counter | Total TCP connections accepted |
| `gt06_connections_active` | Gauge | Currently connected devices |
| `gt06_frames_received_total` | CounterVec(`protocol`) | Frames decoded by protocol code |
| `gt06_location_reports_total` | Counter | Location frames published to stream |
| `gt06_heartbeats_total` | Counter | 0x13 heartbeats received |
| `gt06_login_success_total` | Counter | Approved 0x01 logins |
| `gt06_login_failure_total` | Counter | Rejected 0x01 logins (not approved / not found) |
| `gt06_decode_errors_total` | Counter | CRC failures or parse errors |
| `gt06_sos_alarms_total` | Counter | SOS alarm events |
| `gt06_overspeed_alarms_total` | Counter | Overspeed alarm events |
| `gt06_unknown_messages_total` | Counter | Unrecognised protocol codes |
| `gt06_stream_publish_seconds` | Histogram | Redis XADD latency |

All new observable events must add a corresponding Prometheus counter or gauge — never emit
telemetry without an instrument.

---

## Environment Variables

| Variable | Default | Purpose |
|---|---|---|
| `GT06_TCP_ADDR` | `:7019` | TCP device listener address |
| `GT06_HTTP_ADDR` | `:9091` | Prometheus `/metrics` + `/healthz` |
| `REDIS_HOST` | `redis` | Redis hostname |
| `REDIS_PORT` | `6379` | Redis port |
| `REDIS_PASSWORD` | `` | Redis auth password |
| `REDIS_GT06_DB` | `1` | Redis DB index (keep separate from JT808 DB 0) |
| `REDIS_POOL_SIZE` | `100` | Redis connection pool size |
| `STREAM_KEY` | `gt06:telemetry` | Redis Stream key |
| `STREAM_MAX_LEN` | `100000` | Stream approximate max length |
| `SESSION_PREFIX` | `gt06:session:` | Redis hash key prefix |
| `ONLINE_Z_KEY` | `gt06:online` | Sorted set for online device presence |
| `CMD_CHANNEL` | `gt06:cmd:` | Redis pub/sub command channel prefix |
| `AUTH_TIMEOUT` | `30s` | Login deadline from TCP connect |
| `HEARTBEAT_TIMEOUT` | `3m` | Idle timeout before connection close |
| `WRITE_TIMEOUT` | `10s` | Socket write deadline |
| `DB_ENABLED` | `false` | Enable MySQL device approval lookup |
| `DB_HOST` | `mysql` | MySQL hostname |
| `DB_PORT` | `3306` | MySQL port |
| `DB_USERNAME` | `laravel` | MySQL user |
| `DB_PASSWORD` | `` | MySQL password |
| `DB_DATABASE` | `laravel` | MySQL database name |
| `DB_DEVICE_TYPE_ID` | `2` | `device_types.id` for auto-created GT06 devices |
| `DB_DEVICES_TABLE` | `devices` | Configurable table name |
| `DB_IMEI_COLUMN` | `imei` | IMEI column name |
| `DB_APPROVED_COLUMN` | `is_approved` | Approval flag column |
| `DB_STATUS_COLUMN` | `status` | Status column |
| `DB_TYPE_ID_COLUMN` | `device_type_id` | Device type FK column |
| `DB_NAME_COLUMN` | `name` | Name column |
| `DB_NOTES_COLUMN` | `notes` | Notes column |
| `APP_DEBUG` | `false` | Verbose structured logging |
| `SERVER_ID` | hostname | Replica identity tag in Redis |

---

## Redis Key Layout

| Key pattern | Type | TTL | Contents |
|---|---|---|---|
| `gt06:session:{imei}` | Hash | 24 h | `imei`, `connected_at`, `last_heartbeat` |
| `gt06:online` | ZSet | — | member=IMEI, score=last-heartbeat nanoseconds |
| `gt06:cmd:{imei}` | Pub/Sub channel | — | Outbound command frames (base64 or raw bytes) |
| `gt06:telemetry` | Stream | ~100 k entries | All telemetry events from all GT06 devices |

Redis DB index 1 is reserved for GT06 to avoid key collisions with JT808 (DB 0) and H02 (DB 2)
on shared Redis instances.

---

## Repository Layout

```
server-gt06/
├── go.mod                              module: gt06-server, go 1.23
├── go.sum
├── VERSION
├── .env.example
├── .gitignore
├── .dockerignore
├── docker-compose.yml
├── CLAUDE.md                           ← this file
├── docs/
│   └── gt06.md                         Protocol reference and field tables
├── pkg/protocol/
│   ├── frame.go                        Short/long packet reader + writer + CRC16-IBM
│   ├── types.go                        Protocol number constants, alarm/status bit flags
│   └── decoder.go                      Per-message body decoders (login, GPS, status, alarm…)
└── server/
    ├── Dockerfile
    ├── cmd/main.go                     Entry point: Redis wait → MySQL wait → serve
    └── internal/
        ├── config/config.go            Env-var loader, MySQLDSN builder
        ├── forwarder/stream.go         Redis XADD publisher (PublishLocation, PublishEvent)
        ├── handler/handler.go          Dispatch table for all protocol codes
        ├── metrics/metrics.go          gt06_* Prometheus instruments
        ├── server/tcp.go               TCP listener, per-conn goroutine, HTTP obs server
        ├── session/session.go          Session struct: IMEI, state, write mutex
        ├── session/registry.go         Local map + Redis sync, Heartbeat, PruneStale
        └── store/device.go             MySQL CheckOrCreate (identical contract to JT808)
```

---

## Versioning

Docker images are published on every merge to `main`.
Tags: `latest` + `vMAJOR.MINOR.PATCH` (semver).
Use `VERSION` file as the canonical version source.
