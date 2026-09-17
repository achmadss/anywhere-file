# Spike reports

A spike answers one question that blocks a decision. It ends in a file here, whatever the
answer. A spike that reports "we cannot do this" has done its job and is the most valuable
kind.

Name files `<issue>-<slug>.md`. Keep them short and say four things: the question, what was
tried, what happened (numbers, error text, file and line references, evidence somebody else
can check), and what it means for the plan.

The two reports here were run against Rust code that was removed when the design changed.
Their findings are about the operating systems, not the language, and the Go agent has to
satisfy the same facts:

| Report | What still applies |
|---|---|
| [`keystore.md`](keystore.md) | Windows Credential Manager must persist to the local machine, never a roaming profile. A locked keystore means wait and retry, never generate a new key. Headless Linux gets a 0600 file and says so. |
| [`mdns.md`](mdns.md) | An mDNS instance name over 63 bytes is dropped in silence. UDP 5353 must be bound with address reuse to coexist with Bonjour, Avahi and the Windows resolver. Windows firewall rules are installer work. macOS local-network consent cannot be pre-granted. |
