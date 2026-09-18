package io.anywherefile.client

import java.nio.ByteBuffer
import kotlin.test.Test
import kotlin.test.assertEquals

class LanDiscoveryTest {
    private val id = "b".repeat(64)

    // The answer the agent sends, with the compression pointers a real packet has: the
    // instance name ends in a pointer to the service type, and the SRV and TXT owners are
    // pointers to the instance name.
    @Test
    fun anAnswerFromTheAgentBecomesADevice() {
        val serviceType = name("_anywhere-file", "_tcp", "local") // at offset 12, after the header
        val instance = label("PC1 bbbbbbbb") + pointer(12)
        val host = name("af-bbbbbbbb", "local")
        val packet = header(4) +
            rr(serviceType, 12, instance) + // the instance name lands at offset 49
            rr(pointer(49), 33, shorts(0, 0, 7433) + host) +
            rr(pointer(49), 16, txt("v=2", "id=$id", "name=pc1", "apps=files")) +
            rr(host, 1, byteArrayOf(192.toByte(), 168.toByte(), 1, 20))
        assertEquals(listOf(Device(id, "pc1", listOf("files"), "192.168.1.20:7433")), devicesIn(packet))
    }

    @Test
    fun whatIsNotAnAnswerIsNothing() {
        assertEquals(emptyList(), devicesIn(QUERY), "our own question")
        assertEquals(emptyList(), devicesIn(byteArrayOf(1, 2, 3)), "too short")
        assertEquals(emptyList(), devicesIn(header(1) + rr(pointer(12), 12, pointer(12))), "a name that loops")
        val srvOnly = header(2) + rr(name("_anywhere-file", "_tcp", "local"), 12, label("pc1") + pointer(12)) +
            rr(pointer(49), 33, shorts(0, 0, 7433) + name("af-b", "local"))
        assertEquals(emptyList(), devicesIn(srvOnly), "no A record for the host")
    }

    private fun header(answers: Int) = shorts(0, 0x8400, 0, answers, 0, 0)
    private fun label(s: String) = byteArrayOf(s.length.toByte()) + s.toByteArray()
    private fun name(vararg labels: String) = labels.fold(ByteArray(0)) { acc, l -> acc + label(l) } + 0
    private fun pointer(offset: Int) = byteArrayOf((0xC0 or (offset shr 8)).toByte(), offset.toByte())
    private fun shorts(vararg v: Int) = ByteBuffer.allocate(v.size * 2).apply { v.forEach { putShort(it.toShort()) } }.array()
    private fun txt(vararg strings: String) = strings.fold(ByteArray(0)) { acc, s -> acc + label(s) }
    private fun rr(owner: ByteArray, type: Int, rdata: ByteArray) =
        owner + shorts(type, 1) + ByteArray(4) + shorts(rdata.size) + rdata
}
