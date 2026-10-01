# 0007: Operator tools are existing open source tools, and every user action is traced

**Status:** accepted, 2026-10-01.

## Decision

| Piece | Choice |
|---|---|
| Health and alerts | Grafana, reading the Prometheus metrics and alert rules the control plane already has. |
| Logs and traces | Grafana Loki for logs and Grafana Tempo for traces, fed by an OpenTelemetry collector. All self-hosted next to the control plane. |
| Account management | Appsmith, self-hosted. Its pages call new operator endpoints on the control plane. It never writes to the database directly. |
| Tracing | Each user action is one trace, from the app through the control plane to the PC. |
| Errors from the app and the PC | Sent automatically to the server. A switch in Settings, Advanced, and on the PC's settings page turns it off. |

## Why existing tools

The operator site has one user. Building it in the control plane means design, code and tests
for pages only the owner sees. Grafana already reads the metrics the server exports. Appsmith
builds admin pages from API calls with no frontend code.

Appsmith calls the control plane's operator API, and does not edit tables. That way, actions
such as suspending an account or deleting it follow the same rules as the server, and they show
up in the audit log. A table editor (NocoDB, Directus) was the other choice. It was dropped
because it bypasses those rules.

## Tracing a user's problem

- The app starts a trace for each user action and names its flow, such as `sign_in`,
  `open_folder`, `download`, `upload`, `accept_invite` or `approve_pc`. Each step in the flow is
  a span.
- The trace id travels in the W3C `traceparent` header from the app to the control plane, and
  from there through the tunnel to the agent. It replaces the `X-Request-Id` header that
  `routing.go` sets today.
- Every log line and span has the trace id, the account id, the device id, the flow, the step,
  and for a failure an error code and a reason in plain words.
- On the same Wi-Fi the server is not in the path. The app and the agent send their spans and
  errors to the server later, through a collector endpoint, so those problems are visible too.

To find what went wrong for one person, the operator types their email in Appsmith. Appsmith
looks up the account id and opens Grafana filtered to it. That view lists the person's recent
flows with failures first. A failed flow opens its trace, which shows where it broke (app,
server or PC) and why.

## What is never logged

- File contents, file names and folder names. A file is described by its size and type.
- Passwords, codes, session tokens and invite codes.
- Email addresses. Logs carry the account id, and the email lookup happens in Appsmith. When an
  account is deleted, its logs no longer point to a person.

## Consequences

- The control plane needs operator endpoints for: find an account by email, show its devices,
  guests, plan and sessions, suspend and restore it, give or end Premium by hand, sign it out
  everywhere, and delete it. All of them are recorded in the audit log.
- The app and the agent need an OpenTelemetry exporter and a local queue for when they are
  offline.
- The privacy page must say that error reports are sent, what is in them, how long they are
  kept, and how to turn them off.
- Two to four more containers run next to the server (Grafana, Loki, Tempo, Appsmith, and the
  collector).
