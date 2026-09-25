package io.anywherefile.client

import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonPrimitive
import java.io.File
import java.io.IOException
import java.net.HttpURLConnection
import java.net.URI

// The account over the wire: the control plane's account endpoints, and the session this
// client holds once it has signed in. Every request is written down, with what it answered,
// in client/control-plane.http.

// The control plane this client signs in to. An address is not a secret, so it is a plain
// file beside known-devices, and the sign-in screen is filled in next time.
class ServerAddress(private val file: File) {
    fun read(): String {
        val written = try {
            file.readText().trim()
        } catch (e: IOException) {
            ""
        }
        return written.ifEmpty { DEFAULT_SERVER }
    }

    fun write(server: String) {
        try {
            file.parentFile?.mkdirs()
            file.writeText("$server\n")
        } catch (e: IOException) {
            // Filling the field in next time is all this is for, so failing to write it is
            // not worth a word on the screen.
        }
    }
}

// Where a client looks for a server before anyone has typed one: see DEFAULT_SERVER and
// cleanAddress in Account.kt, which the agent's own address checking matches
// (device/agent/enrol.go).

// What the server said when it would not do something, in its own words. The account pages
// show the same text, so there is one wording to keep right rather than two.
class Refused(message: String) : Exception(message)

@Serializable
private class SignedIn(val token: String = "")

@Serializable
private class Refusal(@SerialName("error") val why: String = "")

@Serializable
private class Identity(val email: String = "")

private val json = Json { ignoreUnknownKeys = true }

// The JSON form of a string, so a password holding a quote or a backslash still makes a body
// the server can read. Two fields are not worth a serializer.
private fun quoted(value: String) = JsonPrimitive(value).toString()

// One address's worth of account endpoints. It holds the address and nothing else, so it is
// built per call.
class ControlPlane(private val base: String) {
    // signIn trades an address and a password for a session token.
    fun signIn(email: String, password: String): String {
        val body = """{"email":${quoted(email)},"password":${quoted(password)}}"""
        val (status, answer) = call("POST", "/v1/auth/signin", body = body)
        if (status != 200) throw refusal(status, answer)
        val token = json.decodeFromString(SignedIn.serializer(), answer).token
        if (token.isEmpty()) throw IllegalStateException("The server did not send a session.")
        return token
    }

    // me answers with the address the session belongs to, or null when the server does not
    // know the session any more. That null is a signed-out session, and the client's job is
    // to stop using the token rather than to ask again.
    fun me(token: String): String? {
        val (status, answer) = call("GET", "/v1/me", token = token)
        if (status == 401) return null
        if (status != 200) throw refusal(status, answer)
        return json.decodeFromString(Identity.serializer(), answer).email
    }

    fun signOut(token: String) {
        val (status, answer) = call("POST", "/v1/auth/signout", token = token)
        // 401 is a session that is already gone, which is what this was asking for.
        if (status != 200 && status != 401) throw refusal(status, answer)
    }

    // call is one request. The timeouts are short, because the person is standing in front of
    // a screen, and not shorter: signing in hashes a password on the server, which is the
    // slowest thing here and still a small fraction of this.
    private fun call(method: String, path: String, body: String? = null, token: String? = null): Pair<Int, String> {
        val conn = URI("$base$path").toURL().openConnection() as HttpURLConnection
        conn.requestMethod = method
        conn.connectTimeout = 5_000
        conn.readTimeout = 15_000
        conn.setRequestProperty("Accept", "application/json")
        if (token != null) conn.setRequestProperty("Authorization", "Bearer $token")
        if (body != null) {
            conn.doOutput = true
            conn.setRequestProperty("Content-Type", "application/json")
        }
        try {
            if (body != null) conn.outputStream.use { it.write(body.encodeToByteArray()) }
            val status = conn.responseCode
            val stream = if (status in 200..299) conn.inputStream else conn.errorStream
            val answer = stream?.use { it.readBytes().decodeToString() }.orEmpty()
            return status to answer
        } finally {
            conn.disconnect()
        }
    }

    // refusal turns an answer into the sentence the person reads. The server writes one for
    // every refusal; anything else is a proxy or a page in the way.
    private fun refusal(status: Int, answer: String): Exception {
        val why = try {
            json.decodeFromString(Refusal.serializer(), answer).why
        } catch (e: Exception) {
            ""
        }
        return if (why.isEmpty()) Refused("The server answered $status.") else Refused(why)
    }
}

// The signed-in account. Writes come from a coroutine off the UI thread; Compose state takes
// them from any thread.
class Session(private val store: SessionStore, private val address: ServerAddress) : Account {
    override var server: String by mutableStateOf(address.read())
        private set

    // The token this client holds, or null when nobody is signed in.
    private var token: String? by mutableStateOf(null)

    override var email: String? by mutableStateOf(null)
        private set

    override var trouble: String? by mutableStateOf(null)
        private set

    override var ready: Boolean by mutableStateOf(false)
        private set

    override val signedIn: Boolean get() = token != null

    override val caveat: String? get() = store.caveat

    // resume picks up the session the last run left. The token is checked against the server
    // once, which is what makes "signed out somewhere else" show up at the next start rather
    // than at the next sign in.
    override suspend fun resume() {
        if (ready) return
        val stored = store.token()
        if (stored == null) {
            ready = true
            return
        }
        try {
            val who = ControlPlane(server).me(stored)
            if (who == null) {
                // The server has no such session: it was signed out, or it expired. The
                // token goes, and the screen says why this time.
                store.forget()
                trouble = "That session ended. Sign in again."
            } else {
                token = stored
                email = who
            }
        } catch (e: Exception) {
            // The server is not answering. Nothing says the session is gone, so the token
            // stays where it is and the account card says what happened.
            token = stored
            trouble = e.message ?: "The server did not answer."
        }
        ready = true
    }

    override suspend fun signIn(server: String, email: String, password: String) {
        val cleaned = cleanAddress(server)
        val who = email.trim().lowercase()
        val token = ControlPlane(cleaned).signIn(who, password)
        address.write(cleaned)
        this.server = cleaned
        store.save(token)
        this.token = token
        this.email = who
        trouble = null
    }

    // signOut ends the session here first and tells the server after. The person asked to be
    // signed out, and a server that cannot be reached does not change that.
    override suspend fun signOut() {
        val going = token
        store.forget()
        token = null
        email = null
        trouble = null
        if (going != null) {
            try {
                ControlPlane(server).signOut(going)
            } catch (e: Exception) {
                // The session is gone from this device, which is what was asked for. The row
                // on the server expires on its own.
            }
        }
    }
}
