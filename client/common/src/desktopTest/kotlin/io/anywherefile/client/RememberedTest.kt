package io.anywherefile.client

import java.io.File
import java.io.IOException
import kotlin.test.AfterTest
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertTrue

// The fallback for when the network answers nothing (#254): PCs remembered from
// connections past are on the list straight away, the ones whose document answers keep
// showing confirmed, and the ones that keep missing are dropped while their address stays
// remembered. The document read is stood in for, the way the agent's own tests stand in
// for the interface list, so nothing here opens a connection.
class RememberedTest {
    private val id = "b".repeat(64)
    private val address = "192.168.1.20:7433"

    // AfterTest gives the network back, since the stand-in is one variable for the whole
    // test compilation.
    @AfterTest
    fun theNetworkIsBack() {
        readDocument = ::readDiscoveryDocument
    }

    private fun file(): File = File.createTempFile("known-devices", "").apply { delete() }

    private fun knownWith(vararg entries: Pair<String, String>): KnownDevices {
        val known = KnownDevices(file())
        entries.forEach { (id, address) -> known.remember(id, address) }
        return known
    }

    // A PC met before is on the list before anything answers, which is the point: the
    // list is not empty while the browse cannot ask the network.
    @Test
    fun aRememberedPCIsOnTheListBeforeAnythingAnswers() {
        val devices = Devices()

        Remembered(knownWith(id to address), devices).round()

        assertEquals(listOf(Device(id, id.take(8), emptyList(), address)), devices.found.toList())
    }

    // The document read is the same check a record from the network gets, so an answering
    // PC shows confirmed, with the name and applications the document carries.
    @Test
    fun anAnsweringPCIsConfirmed() {
        readDocument = { _, _ -> Contact(DiscoveryDocument(v = 2, deviceId = id, name = "pc1", apps = listOf("files")), ByteArray(0)) }
        val known = knownWith(id to address)
        val devices = Devices()

        Remembered(known, devices).round()

        assertEquals(listOf(Device(id, "pc1", listOf("files"), address, confirmed = true)), devices.found.toList())
        assertTrue(known.knew(id), "a PC met before was treated as new")
    }

    // Three rounds of silence and the card goes. The address stays in the file, so the PC
    // is back on the list the moment its document answers again.
    @Test
    fun aSilentPCIsDroppedButStillRemembered() {
        readDocument = { _, _ -> throw IOException("a PC that is not there") }
        val known = knownWith(id to address)
        val devices = Devices()
        val remembered = Remembered(known, devices)

        repeat(3) { remembered.round() }
        assertEquals(emptyList(), devices.found.toList())

        readDocument = { _, _ -> Contact(DiscoveryDocument(v = 2, deviceId = id, name = "pc1"), ByteArray(0)) }
        remembered.round()

        assertEquals(listOf(Device(id, "pc1", emptyList(), address, confirmed = true)), devices.found.toList())
    }

    // A PC that answers once and goes quiet after does not stay on the list forever: the
    // misses are counted from the answer, so the card goes three rounds later.
    @Test
    fun aPCThatGoesSilentAfterAnsweringIsDroppedToo() {
        val known = knownWith(id to address)
        val devices = Devices()
        val remembered = Remembered(known, devices)
        readDocument = { _, _ -> Contact(DiscoveryDocument(v = 2, deviceId = id, name = "pc1"), ByteArray(0)) }
        remembered.round()
        readDocument = { _, _ -> throw IOException("a PC that is not there") }

        repeat(3) { remembered.round() }

        assertEquals(emptyList(), devices.found.toList())
    }
}
