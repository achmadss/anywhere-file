# 0006: The agent configures itself, and a PC is enrolled by approving it in a browser

**Status:** accepted, 2026-09-18. Supersedes the last consequence of
[0005](0005-go-agent-native-tunnel-opaque-sessions.md), which said the client is the only UI,
including for enrolling a PC.

## Decision

| Piece | Choice |
|---|---|
| Settings on a PC | The agent serves a settings page on `127.0.0.1` and takes the same commands from a terminal (#136). A menu bar item on macOS, a tray icon on Windows and a `.desktop` entry on Linux open the page. |
| Who writes `agent.json` | The running agent, always. Both front ends post to one loopback endpoint. |
| Enrolment | The agent asks the server to start an enrolment, signed with its device key, then opens the browser. The person signs in on the website and approves that PC by name. The agent polls, gets a token and enrols (#139). |
| The website | Signup, email verification, password reset, the approval page and the downloads (#137, #138, #139). No account, billing or device management. |
| `/enrol` on the LAN gateway | Removed (#140). It existed for the client to call, and the client no longer enrols anything. |

## Why the agent and not the client

The client is installed on whatever the person carries and can be on a different machine from
the agent, or on a phone. Three things follow:

- A share directory is a path on the serving machine. A client on a phone has no way to
  choose one without the agent enumerating its own filesystem over the network.
- Writing `agent.json` from the client only works in the one case where both happen to be on
  the same PC as the same user.
- A PC with no screen has no client on it at all.

Putting the settings on the agent covers every case with one implementation. The client stays
a consumer, which is what it is.

## Why one endpoint with two front ends

The running agent holds the registry in memory and builds the gateway's routes from it. A
command line that edited `agent.json` on disk would leave the running agent serving the old
list. Watching the file needs a dependency, and signalling the process has no answer on
Windows.

So the command line posts to the same loopback endpoint the page does, and the agent is the
only writer. With the agent stopped the command line writes the file directly, because
nothing is running to fall out of step with it.

Loopback is not an authorization. Every account on a shared PC can reach `127.0.0.1`, so the
endpoint takes a token from a mode 0600 file in the agent's directory, which is the
protection the device seed already has on a machine with no keystore.

## Why approving in a browser

Two alternatives were considered.

A sign-in form on the agent's own settings page would put the account password through the
agent process. It is loopback only and nothing would be stored, and it still asks a person to
type their password into a page at a bare IP address with no padlock, which is the thing
people are told never to do.

Pasting an enrolment token minted elsewhere is what `agent enrol` does today. It works and it
is a step people get wrong, and it needs a second device already signed in.

Approving in a browser reuses the sign-in that has to exist on the website anyway, and the
browser already holds the session, so a second PC is one click. A PC with no screen prints
the code and the person approves it from a phone.

The approval cannot be the whole proof. A `device_id` travels in the mDNS record and in the
discovery document, so it is not a secret, and a link carrying only a device id would let
anyone who has seen a PC on a network enrol it to their own account. The agent therefore
signs the request that starts the enrolment and the request that completes it. Approval alone
binds nothing and the device key alone binds nothing, which is the same split as the OAuth
device authorization grant.

## Consequences

- There is a website. #41 previously said there is no web surface and the client is the only
  UI. That is now wrong in one direction only: the web does signup, verification, reset,
  approval and downloads, and nothing else.
- #101, the client's enrolment screen, is closed. Enrolment lives in #139.
- Accepted risk A1 in the threat model loses its enrolment half once #140 lands. What remains
  of A1 is the use of the applications, which V2 addresses.
- A3 is added to the threat model for the settings endpoint.
- The agent is still a service with no window. The menu bar and tray items open a browser
  rather than drawing anything, so no GUI toolkit enters the Go agent.
