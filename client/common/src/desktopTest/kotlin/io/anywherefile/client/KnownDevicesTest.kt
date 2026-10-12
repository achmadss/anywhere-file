package io.anywherefile.client

import java.io.File
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

class KnownDevicesTest {
    private val id = "c".repeat(64)
    private val other = "d".repeat(64)

    private fun file(): File = File.createTempFile("known-devices", "").apply { delete() }

    // A PC is new once. The answer is what puts "check the fingerprint" on the screen, so
    // asking twice has to say no the second time.
    @Test
    fun aPCIsNewOnlyTheFirstTime() {
        val known = KnownDevices(file())
        assertFalse(known.knew(id))
        assertTrue(known.knew(id))
    }

    // #127's acceptance: forgetting a PC and meeting it again shows it as new, which is the
    // way to be asked to compare the fingerprint a second time.
    @Test
    fun aForgottenPCIsNewAgain() {
        val known = KnownDevices(file())
        known.knew(id)
        known.forget(id)
        assertFalse(known.knew(id), "a forgotten PC was still known")
    }

    // A forgotten PC stays on the screen, marked new. Taking it off and waiting for the
    // browse to bring it back is what the desktop would allow and Android would not: its
    // resolver hands a PC over once and says nothing further until the PC leaves the
    // network.
    @Test
    fun aForgottenPCStaysOnTheList() {
        val devices = Devices()
        val known = KnownDevices(file())
        val device = Device(id, "pc1", listOf("files"), "h:1", confirmed = true)
        devices.seen(device)
        known.knew(id)

        forget(device, devices, known)

        assertEquals(listOf(device.copy(firstContact = true)), devices.found.toList())
        assertFalse(known.knew(id), "a forgotten PC was still known")
    }

    // The file is the point: the app is closed and opened far more often than a PC changes.
    @Test
    fun whatIsKnownOutlastsTheApp() {
        val f = file()
        KnownDevices(f).knew(id)
        assertTrue(KnownDevices(f).knew(id), "a PC met before was new again after a restart")
        assertEquals(listOf(id), f.readLines().filter { it.isNotEmpty() })
    }

    // The address a PC answered at is kept beside its id (#254), so the browse can try it
    // again when the network cannot be asked. Forgetting the PC takes the address with it.
    @Test
    fun theAddressIsRememberedWithThePC() {
        val f = file()
        KnownDevices(f).remember(id, "192.168.1.20:7433")
        assertEquals(mapOf(id to "192.168.1.20:7433"), KnownDevices(f).remembered())
        assertEquals(listOf("$id 192.168.1.20:7433"), f.readLines().filter { it.isNotEmpty() })

        KnownDevices(f).forget(id)
        assertEquals(emptyMap(), KnownDevices(f).remembered())
    }

    // Lines written before the address was kept, with only an id on them, still say the PC
    // is known; they just carry no address to fall back on.
    @Test
    fun anOldLineWithOnlyAnIdIsStillKnown() {
        val f = file()
        f.writeText("$id\n$other old-address:1\n")
        val known = KnownDevices(f)
        assertTrue(known.knew(id), "an id-only line should count as known")
        assertTrue(known.knew(other), "a line with an address should count as known")
        assertEquals(mapOf(other to "old-address:1"), known.remembered())
    }

    // A line is an id, a space and an address. In the unlikely case of two PCs turning
    // up on one line, the first token is the id and the second is whatever is left,
    // which keeps the file one line per PC.
    @Test
    fun aLineIsTheIdThenTheAddress() {
        val f = file()
        f.writeText("$id 192.168.1.20:7433 extra\n")
        assertEquals(mapOf(id to "192.168.1.20:7433 extra"), KnownDevices(f).remembered())
    }

    // A directory nobody may write is a reason to show a PC as new, never a reason to crash
    // on a screen that is otherwise working.
    @Test
    fun aFileThatCannotBeWrittenIsNotACrash() {
        val known = KnownDevices(File("/proc/nowhere/known-devices"))
        assertFalse(known.knew(id))
        known.remember(id, "h:1")
        known.forget(id)
    }
}
