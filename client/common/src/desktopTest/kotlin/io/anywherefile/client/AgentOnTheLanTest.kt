package io.anywherefile.client

import kotlinx.coroutines.runBlocking
import java.io.File
import java.io.InputStream
import java.util.concurrent.ArrayBlockingQueue
import java.util.concurrent.TimeUnit
import kotlin.test.Test
import kotlin.test.assertContentEquals
import kotlin.test.assertEquals
import kotlin.test.assertNull
import kotlin.test.assertTrue
import kotlin.test.fail

// #98's, #127's and #155's acceptance on the desktop: a real agent on this machine is found
// by the browse with TXT intact, its discovery document is read over the gateway's TLS and
// accepted only because the agent proved the certificate is its own, and a file goes up to
// the folder it shares and comes back down byte for byte. It needs an agent running, so it
// does nothing unless RFM_TEST_AGENT_ID says which one to expect, and CI checks that it
// printed "found agent" rather than trusting a green tick.
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
            // The file browser, against the folder that agent shares. Nothing here is a
            // browser or a relay: the client asks dufs for the listing, sends a file and
            // pulls it back, all over the connection it just pinned (#155).
            val sent = "a file the client sent\n".repeat(1000).toByteArray()
            val transfers = TestTransfers("from-the-client.txt", sent)
            val files = openedFiles(confirmed, transfers)
            runBlocking {
                val before = files.list("")
                assertTrue(before.canUpload, "the shared folder refuses uploads")
                assertEquals("Sent from-the-client.txt", files.upload(""))

                val landed = files.list("").entries.firstOrNull { it.name == "from-the-client.txt" }
                    ?: fail("the file is not in the listing")
                assertEquals(sent.size.toLong(), landed.size, "the size in the listing")

                files.download("", landed)
                assertContentEquals(sent, transfers.saved, "what came back down")

                files.delete("", landed)
                assertTrue(
                    files.list("").entries.none { it.name == "from-the-client.txt" },
                    "the file is still there after a delete",
                )
            }
            println("carried a file to agent $want and back")
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

    // openFiles is the client's own way in: it reads the discovery document again, which
    // is what says which certificate key every request after it has to find.
    private fun openedFiles(device: Device, transfers: Transfers): Files {
        val opened = ArrayBlockingQueue<Files>(1)
        openFiles(device, "files", Devices(), transfers) { opened.add(it) }
        return opened.poll(15, TimeUnit.SECONDS) ?: fail("the shared folder did not open")
    }

    private var found: Devices? = null // for the message only
}

// The platform's part, which on a desktop is a file dialog and a Downloads folder. Here
// the file to send is made up and what comes back is kept for comparing.
private class TestTransfers(private val name: String, private val body: ByteArray) : Transfers {
    var saved: ByteArray? = null

    override suspend fun pick() = Outgoing(name, body.size.toLong()) { body.inputStream() }

    override fun save(name: String, body: InputStream): String {
        saved = body.readBytes()
        return "kept in memory"
    }
}
