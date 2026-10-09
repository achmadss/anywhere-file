# The control plane

How to run and test the server while developing it. To host one, see the [README](../README.md#host-the-server).

`hosted/control-plane/` needs PostgreSQL. `hosted/control-plane/docker-compose.yml` brings one up on host
port 5433.

```sh
cd hosted/control-plane
docker compose up -d
export RFM_DATABASE_URL='postgres://rfm:rfm@localhost:5433/rfm?sslmode=disable'
go run . migrate up          # `migrate down [n]` reverses
go run . serve               # :8443, HTTP unless RFM_TLS_CERT and RFM_TLS_KEY are set
curl -s localhost:8443/healthz
```

The Go tests that touch the schema need the same database, under a separate variable so that
a stray `go test` cannot wipe a development one. They drop and recreate the `public` schema,
so point it at a throwaway:

```sh
RFM_TEST_DATABASE_URL='postgres://rfm:rfm@localhost:5433/rfm?sslmode=disable' go test ./...
```

Without it those tests skip, and a skip looks like a pass. CI runs them against PostgreSQL
on the ubuntu runner and fails if the invite race test did not actually run.

## The account pages

The service answers JSON under `/v1` and HTML on six paths. `GET /v1/version` returns the latest release for the agent and the app to compare against. They are the pages a browser
needs, and nothing more: account, billing and device management are client screens.

| Path | What |
|---|---|
| `GET /signup` | create an account, posts to `POST /v1/auth/signup` |
| `GET /verify` | where the link in the confirmation email lands |
| `GET /reset` | ask for a password reset link |
| `GET /reset/confirm` | where the link in that email lands |
| `GET /approve` | let a PC join the account, from the code the agent opens the browser with |
| `GET /download` | the packages on the current GitHub release, the visitor's system first |

Each page is one embedded template and a few lines of JavaScript that post the same JSON
body the client posts, so `/v1` is the only API and there is no form handler behind these.
The token from a link is read out of the query string by the page.

`/download` asks GitHub for the latest release, keeps the answer for ten minutes, and
orders the files by the User-Agent. It has no JavaScript and reads nothing of ours. While
GitHub cannot be reached and nothing has been remembered yet, it links to the releases page
instead of listing nothing.

`/approve` is the one that reads the database before it renders, because it names the PC
that is asking. It signs the visitor in on the page itself, so the code in the URL survives.
Over plain HTTP that needs `RFM_INSECURE_COOKIES`, because the session cookie is otherwise
marked `Secure` and the browser will not send it back.

## The client against this server

The desktop client signs in to `http://127.0.0.1:8443` before anyone types an address, because
that is where this document says the server runs. The Android app has to be told
`http://10.0.2.2:8443`, which is how the emulator reaches the machine it is running on: on a
phone, `127.0.0.1` is the phone.

Cleartext is refused by Android except in debug builds, and there only for `10.0.2.2`,
`127.0.0.1` and `localhost` (see `client/androidApp/src/debug/res/xml/network_security_config.xml`).
A release build needs a server with a certificate the platform trusts, which for a local run
means terminating TLS in front of this process rather than pointing the app at it directly.

Signing in needs an account, and creating one is the website's job: `GET /signup`, which posts
the same JSON body to `/v1/auth/signup` that the client posts to `/v1/auth/signin`.

## Sending mail

Signup and password reset mint a link each. With `RFM_SMTP_ADDR` unset the link goes to the
log rather than to the person, which is what a local run and the tests use. The settings are
in the README.

Signup, locally:

```sh
export RFM_BASE_URL=http://127.0.0.1:8443
go run . serve
curl -s -X POST localhost:8443/v1/auth/signup -H 'Content-Type: application/json' \
  -d '{"email":"you@example.test","password":"correct-horse-123"}'
# the log carries the link; open it in a browser
```

Set `RFM_BASE_URL` wherever a load balancer sits in front. Without it the link is built from
the request, which carries the internal address in that setup and reaches nobody.

## Error reports

A PC on an account sends error reports to `POST /v1/devices/reports`, signed with its device
key (ADR 0007). The server sets the device id and the account id from the key that signed,
and posts the records to `$RFM_OTLP_ENDPOINT/v1/logs` as OTLP JSON. When the collector does
not answer, the PC keeps the reports and sends them again later.

## Tracing

`routing.go` reads the W3C `traceparent` header on a remote request, starts one if it is
missing, forwards it down the tunnel to the agent and logs its trace id. It replaced
`X-Request-Id` (ADR 0007, #209).
