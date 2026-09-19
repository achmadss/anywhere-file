package io.anywherefile.client

import java.io.File
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull
import kotlin.test.assertTrue
import kotlin.test.fail

// #98's and #127's acceptance on the desktop: a real agent on this machine is found by the
// browse with TXT intact, and its discovery document is read over the gateway's TLS and
// accepted only because the agent proved the certificate is its own. It needs an agent
// running, so it does nothing unless RFM_TEST_AGENT_ID says which one to expect, and CI
// checks that it printed "found agent" rather than trusting a green tick.
class AgentOnTheLanTest {
    @Test
    fun theAgentOnThisMachineIsFoundAndItsDocumentRead() {
        val want = System.getenv("RFM_TEST_AGENT_ID")
        if (want.isNullOrEmpty()) {
            println("skipped: RFM_TEST_AGENT_ID is not set")
            return
        }
        val devices = Devices()
        found = devices
        // A file that does not exist, so the agent is a PC this client has never met.
        val known = KnownDevices(File.createTempFile("known-devices", "").apply { delete() })
        val discovery = LanDiscovery(devices, known)
        discovery.start()
        try {
            val device = waitFor("the agent in the browse") { devices.found.firstOrNull { it.id == want } }
            println("found agent ${device.id} as ${device.name} at ${device.address} sharing ${device.apps}")
            assertEquals(listOf("files"), device.apps, "the record's application list")
            val confirmed = waitFor("the discovery document") { devices.found.firstOrNull { it.id == want && it.confirmed } }
            assertEquals(listOf("files"), confirmed.apps, "the document's application list")
            // Nothing is confirmed unless the agent signed its own certificate with the
            // device key, so reaching here is the proof having held.
            assertNull(confirmed.refused, "the agent was turned away")
            assertTrue(confirmed.firstContact, "an agent this client has never met was not new")
            println("proved agent ${confirmed.id} is ${fingerprintOf(confirmed.id)}")
        } finally {
            discovery.stop()
        }
    }

    private fun <T> waitFor(what: String, get: () -> T?): T {
        val deadline = System.currentTimeMillis() + 15_000
        while (System.currentTimeMillis() < deadline) {
            get()?.let { return it }
            Thread.sleep(200)
        }
        fail("$what did not turn up in 15 s, list is ${found?.found?.toList()}")
    }

    private var found: Devices? = null // for the message only
}
