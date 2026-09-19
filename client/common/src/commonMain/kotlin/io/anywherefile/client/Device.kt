package io.anywherefile.client

import androidx.compose.runtime.mutableStateListOf

// LAN discovery (#98). The agent advertises _anywhere-file._tcp with the device id, the
// display name, the application names and the protocol version in TXT, and the gateway
// port in SRV. That record is enough to list a PC; its discovery document is the
// authoritative version and replaces the record's names once it has been read.

const val SERVICE_TYPE = "_anywhere-file._tcp"

// What the agent speaks, device/agent/state.go. A record with another version is a PC
// running something this client cannot talk to, and is left out rather than shown broken.
const val PROTOCOL_VERSION = "2"

data class Device(
    val id: String,
    val name: String,
    val apps: List<String>,
    // host:port of the gateway, where the applications and the discovery document are.
    val address: String,
    // True once name and apps came from the discovery document rather than the record.
    val confirmed: Boolean = false,
    // Why this PC was turned away, when it could not prove it holds the device key it is
    // advertising (#127). Null while it still might.
    val refused: String? = null,
    // True when this client had not connected to this PC before, so the fingerprint on the
    // screen is worth comparing against the PC itself.
    val firstContact: Boolean = false,
)

// A PC that has answered for itself, either way. What the record says about it after that
// changes nothing.
private val Device.answered get() = confirmed || refused != null

// deviceFrom flattens a resolved record the way the agent's own `discover` does. Missing
// pieces are refusals: without an id there is nothing to pin (#127) and nothing to tell
// two PCs apart by.
fun deviceFrom(txt: Map<String, String>, host: String, port: Int): Device? {
    val id = txt["id"] ?: return null
    if (id.length != 64 || txt["v"] != PROTOCOL_VERSION) return null
    val apps = txt["apps"].orEmpty().split(',').filter { it.isNotEmpty() }
    val name = txt["name"].orEmpty().ifEmpty { id.take(8) }
    return Device(id, name, apps, hostPort(host, port))
}

fun hostPort(host: String, port: Int) = if (':' in host) "[$host]:$port" else "$host:$port"

// Devices is the list the screen shows, fed from whichever thread the platform's browser
// calls back on. Compose snapshot state takes writes from any thread.
class Devices {
    val found = mutableStateListOf<Device>()

    // seen adds or replaces by id. A record never downgrades an answer: the record says
    // less, and it keeps arriving for as long as the PC is on the network.
    fun seen(device: Device) {
        val i = found.indexOfFirst { it.id == device.id }
        when {
            i < 0 -> found += device
            device.answered || !found[i].answered -> found[i] = device
        }
    }

    fun lost(id: String) {
        found.removeAll { it.id == id }
    }
}

// Discovery is one platform's browse. Start it when the screen is shown and stop it when
// it is not, so a phone in a pocket is not querying the network.
interface Discovery {
    fun start()
    fun stop()
}
