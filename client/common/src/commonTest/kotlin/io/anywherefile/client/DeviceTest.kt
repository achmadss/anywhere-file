package io.anywherefile.client

import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull

class DeviceTest {
    private val id = "a".repeat(64)

    @Test
    fun aRecordFromTheAgentBecomesADevice() {
        val d = deviceFrom(mapOf("v" to "2", "id" to id, "name" to "pc1", "apps" to "files,notes"), "192.168.1.20", 7433)
        assertEquals(Device(id, "pc1", listOf("files", "notes"), "192.168.1.20:7433"), d)
    }

    @Test
    fun aPCSharingNothingHasNoApps() {
        assertEquals(emptyList(), deviceFrom(mapOf("v" to "2", "id" to id, "name" to "pc1", "apps" to ""), "h", 1)?.apps)
    }

    @Test
    fun whatIsNotOursIsLeftOut() {
        assertNull(deviceFrom(mapOf("v" to "2", "name" to "pc1"), "h", 1), "no id")
        assertNull(deviceFrom(mapOf("v" to "1", "id" to id), "h", 1), "another protocol")
        assertNull(deviceFrom(mapOf("v" to "2", "id" to "abc"), "h", 1), "a short id")
    }

    @Test
    fun theDocumentOutranksTheRecord() {
        val devices = Devices()
        val fromRecord = Device(id, "pc1", listOf("files"), "h:1")
        val fromDocument = fromRecord.copy(apps = listOf("files", "notes"), confirmed = true)
        devices.seen(fromRecord)
        devices.seen(fromDocument)
        devices.seen(fromRecord) // the record keeps arriving while the PC is on the network
        assertEquals(listOf(fromDocument), devices.found.toList())
        devices.lost(id)
        assertEquals(emptyList(), devices.found.toList())
    }
}
