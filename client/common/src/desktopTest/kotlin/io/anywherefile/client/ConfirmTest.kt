package io.anywherefile.client

import java.io.File
import kotlin.test.AfterTest
import kotlin.test.Test
import kotlin.test.assertTrue

// confirm's answer about a PC the browse found for the first time: the document read is
// stood in for, the way RememberedTest's is, so nothing here opens a connection.
class ConfirmTest {
    private val id = "c".repeat(64)
    private val address = "192.168.1.30:7433"

    @AfterTest
    fun theNetworkIsBack() {
        readDocument = ::readDiscoveryDocument
    }

    private fun file(): File = File.createTempFile("known-devices", "").apply { delete() }

    // A client that has never met the PC sees it as a first contact, even though the
    // address is remembered in the same breath as the question was asked.
    @Test
    fun aPCMetForTheFirstTimeIsANewContact() {
        readDocument = { _, _ ->
            Contact(DiscoveryDocument(v = 2, deviceId = id, name = "pc1", apps = listOf("files")), ByteArray(0))
        }
        val devices = Devices()

        confirm(Device(id, "pc1", listOf("files"), address), devices, KnownDevices(file()))

        var confirmed: Device? = null
        val deadline = System.currentTimeMillis() + 5_000
        while (confirmed == null && System.currentTimeMillis() < deadline) {
            confirmed = devices.found.firstOrNull { it.id == id && it.confirmed }
            if (confirmed == null) Thread.sleep(50)
        }
        assertTrue(confirmed?.firstContact == true, "a PC this client has never met was not new")
    }
}
