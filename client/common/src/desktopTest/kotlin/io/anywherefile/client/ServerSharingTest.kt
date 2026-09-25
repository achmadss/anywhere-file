package io.anywherefile.client

import kotlinx.coroutines.runBlocking
import java.io.File
import java.net.HttpURLConnection
import java.net.URI
import java.security.KeyPair
import java.security.KeyPairGenerator
import java.security.MessageDigest
import java.security.SecureRandom
import java.security.Signature
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith
import kotlin.test.assertTrue

// #102 against a live control plane: an owner invites a second account as a guest, the guest
// joins with the code and sees the PC and what it shares, and once the owner removes them the
// PC is gone from their list. Opening the application through the server is #103's.
//
// A PC is signed in the way the agent does it, with a device key and signed requests, so the
// test needs nothing running but the server. Like ServerAccountTest it does nothing unless
// RFM_TEST_SERVER names one, and CI checks it printed "removed the guest".
class ServerSharingTest {
    @Test
    fun aGuestJoinsWithACodeAndIsRemoved() {
        val base = System.getenv("RFM_TEST_SERVER")
        if (base.isNullOrEmpty()) {
            println("skipped: RFM_TEST_SERVER is not set")
            return
        }
        val run = System.nanoTime()
        val owner = session(base, "owner-$run@example.test")
        val guest = session(base, "guest-$run@example.test")
        val pc = enrolPc(base, owner.token, "desk-$run")

        runBlocking {
            owner.session.refresh()
            val mine = owner.session.remote!!.single()
            assertEquals(pc, mine.id)
            assertEquals("admin", mine.role)
            assertEquals(listOf("files"), mine.apps)

            val code = owner.session.invite(pc, "guest", "1h")
            assertEquals("guest", code.role)
            guest.session.redeem(code.code)
            println("joined with a code")

            // The guest sees the PC and what it shares, and none of the management.
            guest.session.refresh()
            val theirs = guest.session.remote!!.single()
            assertEquals(pc, theirs.id)
            assertEquals("guest", theirs.role)
            assertEquals(listOf("files"), theirs.apps)
            assertFailsWith<Refused> { guest.session.users(pc) }
            assertFailsWith<Refused> { guest.session.invite(pc, "guest", "1h") }

            // The code worked once.
            assertFailsWith<Refused> { guest.session.redeem(code.code) }

            val people = owner.session.users(pc)
            assertEquals(setOf("admin", "guest"), people.map { it.role }.toSet())
            owner.session.revoke(pc, people.single { it.role == "guest" }.id)
            guest.session.refresh()
            assertTrue(guest.session.remote!!.isEmpty(), "the PC is still on the guest's list")
            assertEquals(1, owner.session.users(pc).size, "a removed guest is still listed")
            println("removed the guest")
        }
    }

    private class Signed(val session: Session, val token: String)

    // session makes an account and signs a client in to it.
    private fun session(base: String, email: String): Signed {
        assertEquals(200, post(base, "/v1/auth/signup", """{"email":"$email","password":"$PASSWORD"}""").first)
        val held = Held()
        val session = Session(held, ServerAddress(File.createTempFile("server", "").apply { delete() }))
        runBlocking { session.signIn(base, email, PASSWORD) }
        return Signed(session, held.token()!!)
    }

    // enrolPc signs a new PC in to the account and tells the server it shares "files", which
    // is what the agent does on `agent enrol` (device/agent/enrol.go).
    private fun enrolPc(base: String, token: String, name: String): String {
        val (status, minted) = post(base, "/v1/devices/enrolment-token", "", token = token)
        assertEquals(200, status, minted)
        val enrolment = Regex("\"token\":\"([^\"]+)\"").find(minted)!!.groupValues[1]
        val key = KeyPairGenerator.getInstance("Ed25519").generateKeyPair()
        val body = """{"public_key":"${hex(raw(key))}","name":"$name","enrolment_token":"$enrolment"}"""
        val (enrolled, answer) = post(base, "/v1/devices/enrol", body, key = key)
        assertEquals(200, enrolled, answer)
        val (synced, apps) = post(base, "/v1/devices/apps", """{"apps":[{"name":"files","type":"dufs"}]}""", key = key)
        assertEquals(200, synced, apps)
        return Regex("\"device_id\":\"([0-9a-f]+)\"").find(answer)!!.groupValues[1]
    }

    // post sends one request, signed by a device key the way internal/devicesig signs one
    // when a key is given.
    private fun post(base: String, path: String, body: String, token: String? = null, key: KeyPair? = null): Pair<Int, String> {
        val conn = URI(base + path).toURL().openConnection() as HttpURLConnection
        conn.requestMethod = "POST"
        conn.doOutput = true
        conn.connectTimeout = 15_000
        conn.readTimeout = 15_000
        conn.setRequestProperty("Content-Type", "application/json")
        if (token != null) conn.setRequestProperty("Authorization", "Bearer $token")
        if (key != null) {
            val public = hex(raw(key))
            val nonce = hex(ByteArray(16).also { SecureRandom().nextBytes(it) })
            val stamp = (System.currentTimeMillis() / 1000).toString()
            val digest = hex(MessageDigest.getInstance("SHA-256").digest(body.encodeToByteArray()))
            val signer = Signature.getInstance("Ed25519").apply { initSign(key.private) }
            signer.update(listOf("POST", path, public, nonce, stamp, digest).joinToString("\n").encodeToByteArray())
            conn.setRequestProperty("X-Device-Key", public)
            conn.setRequestProperty("X-Device-Nonce", nonce)
            conn.setRequestProperty("X-Device-Timestamp", stamp)
            conn.setRequestProperty("X-Device-Signature", hex(signer.sign()))
        }
        conn.outputStream.use { it.write(body.encodeToByteArray()) }
        val status = conn.responseCode
        val answer = (if (status in 200..299) conn.inputStream else conn.errorStream)?.use { it.readBytes().decodeToString() }.orEmpty()
        conn.disconnect()
        return status to answer
    }

    // The JDK hands a public key out wrapped in its X.509 header, and the key itself is the
    // last 32 bytes of that.
    private fun raw(key: KeyPair) = key.public.encoded.takeLast(32).toByteArray()

    private fun hex(bytes: ByteArray) = bytes.joinToString("") { "%02x".format(it) }
}
