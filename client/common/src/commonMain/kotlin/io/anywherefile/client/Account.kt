package io.anywherefile.client

// The account half of this client (#100), as the screens see it. Signing in is one call to
// the control plane, which answers with an opaque session token: it is returned once, this
// client keeps it, and every request that needs an account carries it as a bearer header.
// Nothing here trusts a token because it decodes (ADR 0005), so there is nothing to check on
// this side but the answer.
//
// The screens are in commonMain and the wire is in jvmMain, so this is the same seam Files
// has: Session, in src/jvmMain, is the one implementation.

interface Account {
    // The control plane in use, which is what the sign-in screen fills its address field with.
    val server: String

    // Who is signed in, once the server has said. Null with a token kept means the server
    // could not be asked, not that nobody is signed in.
    val email: String?

    val signedIn: Boolean

    // The last thing that went wrong, in words for the screen.
    val trouble: String?

    // True once a stored session has been looked at, so a signed-in screen does not flash
    // "signed out" while the token is still being checked.
    val ready: Boolean

    // A sentence about where the token is kept, or null when it is in a secret store.
    val caveat: String?

    // resume picks up the session the last run left and checks it against the server once.
    suspend fun resume()

    // signIn signs in and keeps the token. It throws what went wrong, in the server's words
    // where it gave any, so the screen can put it in front of the person.
    suspend fun signIn(server: String, email: String, password: String)

    suspend fun signOut()

    // The PCs this account may reach from outside the house (#102), as the server last
    // listed them. Null until it has been asked. A PC this account was removed from is left
    // out, so it is gone from the screen at the next refresh.
    val remote: List<RemoteDevice>?

    suspend fun refresh()

    // What an admin of a PC does with it. The server refuses anyone else with the answer an
    // unknown PC gets, so a guest is never shown these to begin with.
    suspend fun users(device: String): List<DeviceUser>
    suspend fun revoke(device: String, user: String)
    suspend fun invite(device: String, role: String, expiresIn: String): Invitation

    // redeem turns a code somebody shared into access to their PC.
    suspend fun redeem(code: String)
}

data class RemoteDevice(
    val id: String,
    val name: String,
    // Whether the PC has a tunnel open to the server right now.
    val online: Boolean,
    // "admin" or "guest".
    val role: String,
    val apps: List<String>,
)

data class DeviceUser(val id: String, val email: String, val role: String)

// A code as it was made. It is shown and shared once, and the server keeps only its hash, so
// there is no asking for it again.
data class Invitation(val code: String, val role: String, val until: String)

// Where this platform keeps the session token. Android has the Keystore and the desktop has
// whatever its system has.
interface SessionStore {
    fun token(): String?
    fun save(token: String)
    fun forget()

    // A sentence for the screen when the token is not in a secret store, or null when it is.
    // A machine with no keychain still has to work; it does not have to pretend.
    val caveat: String? get() = null
}

// Where a client looks for a server before anyone has typed one. There is no hosted
// deployment yet, and this is the address the control plane runs on locally
// (docs/running-the-control-plane.md). On a phone this has to be typed, because the loopback
// address is the phone itself.
const val DEFAULT_SERVER = "http://127.0.0.1:8443"

// cleanAddress takes what a person typed and returns the origin to talk to, the way the agent
// cleans the address it is given (device/agent/enrol.go). Anything after the host is a sign
// the wrong thing was pasted, so it is refused rather than half used.
fun cleanAddress(raw: String): String {
    val typed = raw.trim().trimEnd('/')
    if (typed.isEmpty()) throw IllegalStateException("Type the address of your server first.")
    val scheme = when {
        typed.startsWith("https://") -> "https"
        typed.startsWith("http://") -> "http"
        else -> throw IllegalStateException("The address has to start with http:// or https://.")
    }
    val rest = typed.removePrefix("$scheme://")
    val host = rest.substringBefore('/').substringBefore('?').substringBefore('#')
    if (host.isEmpty()) throw IllegalStateException("The address has no host in it.")
    if (host != rest) throw IllegalStateException("Only the address goes here, with nothing after it.")
    return "$scheme://$host"
}
