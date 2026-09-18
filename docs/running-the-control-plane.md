# Running the control plane locally

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

The service answers JSON under `/v1` and HTML on four paths. They are the pages a browser
needs, and nothing more: account, billing and device management are client screens.

| Path | What |
|---|---|
| `GET /signup` | create an account, posts to `POST /v1/auth/signup` |
| `GET /verify` | where the link in the confirmation email lands |
| `GET /reset` | ask for a password reset link |
| `GET /reset/confirm` | where the link in that email lands |

Each page is one embedded template and a few lines of JavaScript that post the same JSON
body the client posts, so `/v1` is the only API and there is no form handler behind these.
The token from a link is read out of the query string by the page.

## Sending mail

Signup and password reset mint a link each. With `RFM_SMTP_ADDR` unset the link goes to the
log rather than to the person, which is what a local run and the tests use. Set it and the
same link goes out as a message.

| Variable | Default | What |
|---|---|---|
| `RFM_ADDR` | `:8443` | the address to listen on |
| `RFM_DATABASE_URL` | none, required | PostgreSQL |
| `RFM_TLS_CERT`, `RFM_TLS_KEY` | empty | serve HTTPS directly, instead of terminating TLS in front |
| `RFM_BASE_URL` | taken from the request | the address the links in a message point at |
| `RFM_SMTP_ADDR` | empty | `host:port` of the SMTP server, empty logs the message instead |
| `RFM_SMTP_USER`, `RFM_SMTP_PASSWORD` | empty | credentials, when the server wants them |
| `RFM_MAIL_FROM` | `no-reply@localhost` | the From address |
| `RFM_INSECURE_COOKIES` | empty | drop the Secure flag from the session cookie, for plain HTTP locally |
| `RFM_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error` |
| `RFM_SHUTDOWN_TIMEOUT` | `20s` | how long to drain in-flight requests |

Set `RFM_BASE_URL` wherever a load balancer sits in front. Without it the link is built from
the request, which carries the internal address in that setup and reaches nobody.

The whole path, locally:

```sh
export RFM_BASE_URL=http://127.0.0.1:8443
go run . serve
curl -s -X POST localhost:8443/v1/auth/signup -H 'Content-Type: application/json' \
  -d '{"email":"you@example.test","password":"correct-horse-123"}'
# the log carries the link; open it in a browser
```
