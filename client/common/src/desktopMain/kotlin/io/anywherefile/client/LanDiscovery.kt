package io.anywherefile.client

import java.io.IOException
import java.net.DatagramPacket
import java.net.DatagramSocket
import java.net.InetAddress
import java.net.InetSocketAddress
import java.net.SocketException
import java.net.SocketTimeoutException
import java.nio.ByteBuffer
import kotlin.concurrent.thread

// The desktop browse. It asks from a port of its own rather than joining 5353 as a full
// mDNS participant. RFC 6762 calls that a legacy query, and a responder answers it by
// unicast to the port it came from. Port 5353 on a desktop already belongs to Bonjour,
// Avahi or the Windows resolver, and the agent answers every query by unicast to its
// source port, so an answer to a query sent from 5353 lands on whichever of the sockets
// bound there the kernel picks. A port nobody else holds gets every answer.
//
// ponytail: one socket, sending on the interface the kernel routes multicast to. A PC with
// a VPN or a second LAN needs one query per interface.
class LanDiscovery(private val devices: Devices, private val known: KnownDevices) : Discovery {
    @Volatile private var socket: DatagramSocket? = null

    override fun start() {
        thread(isDaemon = true, name = "discovery") {
            val s = DatagramSocket()
            socket = s
            s.soTimeout = 250
            val group = InetSocketAddress(InetAddress.getByName(MDNS_GROUP), MDNS_PORT)
            val buf = ByteArray(9000)
            val lastSeen = HashMap<String, Long>()
            var nextQuery = 0L
            try {
                while (!s.isClosed) {
                    val now = System.currentTimeMillis()
                    if (now >= nextQuery) {
                        nextQuery = now + QUERY_INTERVAL_MS
                        try {
                            s.send(DatagramPacket(QUERY, QUERY.size, group))
                        } catch (e: IOException) {
                            // No route to the group, or the OS refused. Ask again next time.
                        }
                        val gone = lastSeen.filterValues { now - it > LOST_AFTER_MS }.keys
                        gone.forEach { lastSeen.remove(it); devices.lost(it) }
                    }
                    val p = DatagramPacket(buf, buf.size)
                    try {
                        s.receive(p)
                    } catch (e: SocketTimeoutException) {
                        continue
                    }
                    val from = p.address?.hostAddress ?: continue
                    for (d in devicesIn(buf.copyOf(p.length), from)) {
                        // A PC already on the list has been asked for its document. One
                        // that has just been forgotten is off the list, so it is asked
                        // again and shown as new.
                        val listed = devices.found.any { it.id == d.id }
                        lastSeen[d.id] = now
                        devices.seen(d)
                        if (!listed) confirm(d, devices, known)
                    }
                }
            } catch (e: SocketException) {
                // stop() closed the socket.
            }
        }
    }

    override fun stop() {
        socket?.close()
        socket = null
    }
}

const val MDNS_GROUP = "224.0.0.251"
const val MDNS_PORT = 5353
const val QUERY_INTERVAL_MS = 2_000L
// A PC that has not answered five queries in a row is off the network or off.
const val LOST_AFTER_MS = 10_000L

private const val TYPE_A = 1
private const val TYPE_PTR = 12
private const val TYPE_TXT = 16
private const val TYPE_SRV = 33

// A PTR question for the service type, the same packet `agent discover` sends.
internal val QUERY: ByteArray = ByteBuffer.allocate(64).run {
    putShort(0); putShort(0); putShort(1); putShort(0); putShort(0); putShort(0)
    for (label in "$SERVICE_TYPE.local".split('.')) {
        put(label.length.toByte())
        put(label.toByteArray())
    }
    put(0.toByte()); putShort(TYPE_PTR.toShort()); putShort(1)
    array().copyOf(position())
}

// devicesIn reads one answer. An instance of our service becomes a device when the packet
// also carries its SRV, its TXT and an A record for the SRV target, which is how the agent
// answers. Anything else in the packet, and any packet that is not an answer or is cut
// short, yields nothing: it came off the network and is trusted for nothing.
//
// The address comes from where the answer was sent from, not from the A records in it. A PC
// advertises every interface it has, so a machine running docker or a VPN offers addresses
// that route nowhere from here, and nothing in the packet says which is which. The source
// address is the one that just carried a packet to us, so it is the one that works.
internal fun devicesIn(packet: ByteArray, source: String): List<Device> = try {
    parse(packet, source)
} catch (e: RuntimeException) {
    emptyList()
}

private fun parse(packet: ByteArray, source: String): List<Device> {
    val b = ByteBuffer.wrap(packet)
    b.position(2)
    if (b.short.toInt() and 0x8000 == 0) return emptyList() // a question, not an answer
    val counts = IntArray(4) { b.short.toInt() and 0xffff }
    repeat(counts[0]) { name(b); b.position(b.position() + 4) }
    val instances = ArrayList<String>()
    val srv = HashMap<String, Pair<String, Int>>() // instance to host name and port
    val txt = HashMap<String, Map<String, String>>()
    val hasAddress = HashSet<String>() // host names that came with an A record
    repeat(counts[1] + counts[2] + counts[3]) {
        val owner = name(b)
        val type = b.short.toInt() and 0xffff
        b.short // class, with the cache-flush bit
        b.int // TTL
        val length = b.short.toInt() and 0xffff
        val end = b.position() + length
        when (type) {
            TYPE_PTR -> if (owner == "$SERVICE_TYPE.local.") instances += name(b)
            TYPE_SRV -> {
                b.short; b.short // priority and weight
                val port = b.short.toInt() and 0xffff
                srv[owner] = name(b) to port
            }
            TYPE_TXT -> txt[owner] = txtStrings(b, end)
            TYPE_A -> if (end - b.position() == 4) hasAddress += owner
        }
        b.position(end)
    }
    return instances.mapNotNull { instance ->
        val (host, port) = srv[instance] ?: return@mapNotNull null
        if (host !in hasAddress) return@mapNotNull null
        deviceFrom(txt[instance].orEmpty(), source, port)
    }
}

// name reads a DNS name, following compression pointers, and leaves the buffer after the
// name as it appeared in the packet. Names are case-insensitive, so they come back lower.
private fun name(b: ByteBuffer): String {
    val sb = StringBuilder()
    var pos = b.position()
    var after = -1
    var hops = 0
    while (true) {
        val len = b.get(pos).toInt() and 0xff
        when {
            len == 0 -> {
                pos++
                break
            }
            len and 0xC0 == 0xC0 -> {
                if (after < 0) after = pos + 2
                pos = ((len and 0x3f) shl 8) or (b.get(pos + 1).toInt() and 0xff)
                require(++hops < 64) { "name loops" }
            }
            else -> {
                sb.append(String(b.array(), pos + 1, len, Charsets.UTF_8)).append('.')
                pos += 1 + len
            }
        }
    }
    b.position(if (after < 0) pos else after)
    return sb.toString().lowercase()
}

private fun txtStrings(b: ByteBuffer, end: Int): Map<String, String> {
    val m = HashMap<String, String>()
    while (b.position() < end) {
        val len = b.get().toInt() and 0xff
        val s = String(b.array(), b.position(), len, Charsets.UTF_8)
        b.position(b.position() + len)
        val eq = s.indexOf('=')
        if (eq > 0) m[s.substring(0, eq)] = s.substring(eq + 1)
    }
    return m
}
