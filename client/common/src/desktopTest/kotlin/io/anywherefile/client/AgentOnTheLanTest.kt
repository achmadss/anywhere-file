package io.anywherefile.client

import java.io.File
import java.net.HttpURLConnection
import java.net.URI
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull
import kotlin.test.assertTrue
import kotlin.test.fail

// #98's, #127's and #99's acceptance on the desktop: a real agent on this machine is found
// by the browse with TXT intact, its discovery document is read over the gateway's TLS and
// accepted only because the agent proved the certificate is its own, and a plain HTTP
// request on loopback reaches it through the relay. It needs an agent running, so it does
// nothing unless RFM_TEST_AGENT_ID says which one to expect, and CI checks that it printed
// "found agent" rather than trusting a green tick.
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
            // The relay is a pipe, so what goes down it is whatever the gateway serves.
            // The document is the one thing the agent always has, with or without an
            // application behind it, and it is the same pipe an application's page uses.
            val relay = relayFor(confirmed)
            val conn = URI("http://127.0.0.1:${relay.port}/.well-known/anywhere-file").toURL()
                .openConnection() as HttpURLConnection
            val body = conn.inputStream.use { it.readBytes().decodeToString() }
            assertEquals(200, conn.responseCode, "the relay's answer")
            assertTrue(want in body, "the document came back through the relay, got $body")
            println("relayed to agent $want on 127.0.0.1:${relay.port}")
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
