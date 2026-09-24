package io.anywherefile.client

import kotlinx.coroutines.runBlocking
import java.io.File
import java.net.HttpURLConnection
import java.net.URI
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertFalse
import kotlin.test.assertNull
import kotlin.test.assertTrue

// #100 against a live control plane: an account is made the way the sign-up page makes one,
// the client signs in, the next run of the client finds the session again, and a session the
// server has ended is noticed at the next start rather than at the next sign-in.
//
// It needs a server, so it does nothing unless RFM_TEST_SERVER names one, and CI checks that
// it printed "signed in" rather than trusting a green tick.
class ServerAccountTest {
    @Test
    fun aSessionIsKeptUntilTheServerEndsIt() {
        val base = System.getenv("RFM_TEST_SERVER")
        if (base.isNullOrEmpty()) {
            println("skipped: RFM_TEST_SERVER is not set")
            return
        }
        // A fresh address each run, so the test does not depend on what is already there.
        val who = "client-test-${System.nanoTime()}@example.test"
        assertEquals(200, post("$base/v1/auth/signup", """{"email":"$who","password":"$PASSWORD"}"""))
        println("made an account for $who")

        val api = ControlPlane(base)
        val token = api.signIn(who, PASSWORD)
        assertEquals(43, token.length, "the shape of a session token")
        assertEquals(who, api.me(token), "who the session belongs to")
        println("signed in as $who")
        // The session this check opened is closed again, so a test run leaves no live session
        // behind and signing out is exercised at the wire too.
        api.signOut(token)
        assertNull(api.me(token), "the server still knows a session that was signed out")
        println("signed out over the wire as $who")

        // A wrong password and an address with no account answer the same, in the server's
        // own words.
        val wrong = assertFailsWith<Refused> { api.signIn(who, "not-the-password-1") }
        assertEquals("invalid email or password", wrong.message)

        // The client's half, over a store that stands in for the platform's. The address
        // file is real, because that part is the same on every platform.
        val held = Held()
        val address = ServerAddress(File.createTempFile("server", "").apply { delete() })
        val session = Session(held, address)
        runBlocking {
            session.resume()
            assertFalse(session.signedIn, "an empty store signed somebody in")

            session.signIn(base, who, PASSWORD)
            assertTrue(session.signedIn, "signing in left the session signed out")
            assertEquals(who, session.email)
            assertEquals(base, address.read(), "the address is not kept for next time")

            // The next run of the app reads the same store.
            val next = Session(held, address)
            next.resume()
            assertTrue(next.signedIn, "the session did not survive a restart")
            assertEquals(who, next.email, "who the stored session belongs to")

            // Signing out ends it here and on the server.
            val ended = held.token()
            next.signOut()
            assertFalse(next.signedIn, "signing out left the session signed in")
            assertNull(held.token(), "the token is still in the store after signing out")
            assertNull(api.me(ended!!), "the server still knows a session that was signed out")
            println("signed out as $who")
        }

        // And the other way round: a session the server ended is gone at the next start,
        // which is the third acceptance case of #100.
        val current = api.signIn(who, PASSWORD)
        held.save(current)
        runBlocking {
            val running = Session(held, address)
            running.resume()
            assertTrue(running.signedIn, "a live session was not picked up")

            api.signOut(current)
            val after = Session(held, address)
            after.resume()
            assertFalse(after.signedIn, "a session the server ended was still signed in")
            assertNull(held.token(), "the token of an ended session was left in the store")
            assertEquals("That session ended. Sign in again.", after.trouble)
            println("noticed a session the server ended")
        }
    }

    // post makes the one request this client never makes itself: creating an account, which
    // is the sign-up page's job (client/control-plane.http). The timeout is generous because
    // this runs beside the rest of the build, and creating an account hashes a password.
    private fun post(url: String, body: String): Int {
        val conn = URI(url).toURL().openConnection() as HttpURLConnection
        conn.requestMethod = "POST"
        conn.doOutput = true
        conn.connectTimeout = 15_000
        conn.readTimeout = 15_000
        conn.setRequestProperty("Content-Type", "application/json")
        conn.outputStream.use { it.write(body.encodeToByteArray()) }
        return conn.responseCode.also { conn.disconnect() }
    }
}

private const val PASSWORD = "correct-horse-123"

// A session store that holds the token for as long as the test runs. What each platform
// really does with it is the subject of SessionsTest.
private class Held : SessionStore {
    private var held: String? = null

    override fun token(): String? = held

    override fun save(token: String) {
        held = token
    }

    override fun forget() {
        held = null
    }
}
