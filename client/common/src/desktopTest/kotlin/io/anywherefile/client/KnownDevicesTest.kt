package io.anywherefile.client

import java.io.File
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFalse
import kotlin.test.assertTrue

class KnownDevicesTest {
    private val id = "c".repeat(64)

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

    // The file is the point: the app is closed and opened far more often than a PC changes.
    @Test
    fun whatIsKnownOutlastsTheApp() {
        val f = file()
        KnownDevices(f).knew(id)
        assertTrue(KnownDevices(f).knew(id), "a PC met before was new again after a restart")
        assertEquals(listOf(id), f.readLines().filter { it.isNotEmpty() })
    }

    // A directory nobody may write is a reason to show a PC as new, never a reason to crash
    // on a screen that is otherwise working.
    @Test
    fun aFileThatCannotBeWrittenIsNotACrash() {
        val known = KnownDevices(File("/proc/nowhere/known-devices"))
        assertFalse(known.knew(id))
        known.forget(id)
    }
}
