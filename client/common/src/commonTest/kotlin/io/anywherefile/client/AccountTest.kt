package io.anywherefile.client

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith

// What a person may type into the address field, and what comes out the other side (#100).
// The rule is the agent's own (device/agent/enrol.go): an origin, and nothing after it.
class AccountTest {
    @Test
    fun anAddressIsKeptAsItIs() {
        assertEquals("https://anywhere.example", cleanAddress("https://anywhere.example"))
        assertEquals("http://127.0.0.1:8443", cleanAddress("http://127.0.0.1:8443"))
    }

    @Test
    fun spacesAndATrailingSlashComeOff() {
        assertEquals("http://127.0.0.1:8443", cleanAddress("  http://127.0.0.1:8443/  "))
    }

    @Test
    fun anAddressWithNoSchemeIsRefused() {
        // A host and a port with no scheme is the most likely thing to be typed by mistake,
        // and http:// guessed for it would send a session token somewhere it was not meant
        // to go.
        val refused = assertFailsWith<IllegalStateException> { cleanAddress("127.0.0.1:8443") }
        assertEquals("The address has to start with http:// or https://.", refused.message)
    }

    @Test
    fun anAddressWithNoHostIsRefused() {
        assertFailsWith<IllegalStateException> { cleanAddress("https://") }
        assertFailsWith<IllegalStateException> { cleanAddress("") }
        assertFailsWith<IllegalStateException> { cleanAddress("   ") }
    }

    @Test
    fun anythingAfterTheHostIsRefusedRatherThanDropped() {
        // A pasted link is the usual way this happens, and quietly cutting it down to the
        // server would sign the person in to something they did not point at.
        val path = assertFailsWith<IllegalStateException> { cleanAddress("https://anywhere.example/signup?token=x") }
        assertEquals("Only the address goes here, with nothing after it.", path.message)
        assertFailsWith<IllegalStateException> { cleanAddress("https://anywhere.example/files") }
        assertFailsWith<IllegalStateException> { cleanAddress("https://anywhere.example?token=x") }
        assertFailsWith<IllegalStateException> { cleanAddress("https://anywhere.example#top") }
    }
}
