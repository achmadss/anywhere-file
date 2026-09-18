package io.anywherefile.client

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.fail

// #98's acceptance on the desktop: a real agent on this machine is found by jmdns with TXT
// intact, and its discovery document is read over the gateway's TLS. It needs an agent
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
        val discovery = JmdnsDiscovery(devices)
        discovery.start()
        try {
            val device = waitFor("the agent in the browse") { devices.found.firstOrNull { it.id == want } }
            println("found agent ${device.id} as ${device.name} at ${device.address} sharing ${device.apps}")
            assertEquals(listOf("files"), device.apps, "the record's application list")
            val confirmed = waitFor("the discovery document") { devices.found.firstOrNull { it.id == want && it.confirmed } }
            assertEquals(listOf("files"), confirmed.apps, "the document's application list")
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
        fail("$what did not turn up in 15 s, list is ${devices()}")
    }

    private fun devices() = "" // for the message only
}
