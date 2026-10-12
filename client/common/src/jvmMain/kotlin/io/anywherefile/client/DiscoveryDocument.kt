package io.anywherefile.client

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import java.net.URI
import java.security.cert.X509Certificate
import javax.net.ssl.HttpsURLConnection
import javax.net.ssl.SSLContext
import javax.net.ssl.X509TrustManager
import kotlin.concurrent.thread

// The gateway's discovery document, GET /.well-known/anywhere-file. It is the
// authoritative application list; the mDNS record's is a convenience that stops at 200
// bytes. It also carries the PC's device key and that key's signature over the certificate
// this was read over, which is what says the PC is the one being looked for (#127).
@Serializable
data class DiscoveryDocument(
    val v: Int,
    @SerialName("device_id") val deviceId: String,
    val name: String,
    val apps: List<String> = emptyList(),
    @SerialName("public_key") val publicKey: String = "",
    @SerialName("tls_proof") val tlsProof: String = "",
)

// What one read of a PC learned: what the document says, and the public key of the
// certificate the PC proved is its own. The key is what every request the file browser
// makes afterwards has to find on the other end (#155).
class Contact(val doc: DiscoveryDocument, val certificateKey: ByteArray)

private val json = Json { ignoreUnknownKeys = true }

// No authority signs the gateway's certificate, so the platform's trust store has nothing
// to check it against and the handshake has to accept it as it stands. The check happens
// straight after, against the key in the document the connection just carried, which is the
// only order it can happen in: the document is what says which key to expect. Nothing is
// sent up before it passes. The request is a GET of a document the PC serves to anyone, and
// the answer is thrown away unless the signature holds.
internal val lanTls: SSLContext = SSLContext.getInstance("TLS").apply {
    val unchecked = object : X509TrustManager {
        override fun checkClientTrusted(chain: Array<X509Certificate>, authType: String) {}
        override fun checkServerTrusted(chain: Array<X509Certificate>, authType: String) {}
        override fun getAcceptedIssuers(): Array<X509Certificate> = arrayOf()
    }
    init(null, arrayOf(unchecked), null)
}

// readDiscoveryDocument returns the document only when the PC proved it holds deviceId's
// key. It throws WrongDevice when the PC is somebody else, and an IOException when the PC
// is not there or answered with something else.
fun readDiscoveryDocument(address: String, deviceId: String): Contact {
    val conn = URI("https://$address/.well-known/anywhere-file").toURL().openConnection() as HttpsURLConnection
    conn.sslSocketFactory = lanTls.socketFactory
    // The certificate names the device, under a suffix that resolves nowhere, and the
    // address it was reached at comes from mDNS and changes with the network. The name is
    // no use here, and the signature checked below is what the name would have been for.
    conn.setHostnameVerifier { _, _ -> true }
    conn.connectTimeout = 3_000
    conn.readTimeout = 3_000
    conn.inputStream.use { body ->
        val doc = json.decodeFromString(DiscoveryDocument.serializer(), body.readBytes().decodeToString())
        val certificate = conn.serverCertificates.firstOrNull() as? X509Certificate
            ?: throw WrongDevice("This device did not identify itself.")
        verifyDeviceProof(deviceId, doc.publicKey, doc.tlsProof, certificate)
        return Contact(doc, certificate.publicKey.encoded)
    }
}

// confirm reads the document for a PC the browse found and replaces what the record said.
// A PC that cannot prove it holds the device key stays on the list with the reason, because
// a PC that has quietly turned into another one is worth seeing.
fun confirm(device: Device, devices: Devices, known: KnownDevices) {
    thread(isDaemon = true, name = "discovery-document ${device.id.take(8)}") {
        val doc = try {
            readDiscoveryDocument(device.address, device.id).doc
        } catch (e: WrongDevice) {
            devices.seen(device.copy(refused = e.message))
            return@thread
        } catch (e: Exception) {
            return@thread
        }
        known.remember(device.id, device.address)
        devices.seen(
            device.copy(
                name = doc.name,
                apps = doc.apps,
                confirmed = true,
                firstContact = !known.knew(device.id),
            ),
        )
    }
}

// readDocument stands where the network is, so tests can hand back an answer without
// opening a connection. Everything that trusts the PC does it through here.
internal var readDocument: (address: String, deviceId: String) -> Contact = ::readDiscoveryDocument

// Remembered keeps the PCs met before reachable when the browse cannot ask the network
// anything (#254). Multicast is refused (macOS's local network permission), or the access
// point isolates its clients, and the PC met yesterday is still there. Its address comes
// from the last confirmed read of its document, and probing is that same read: the
// document is what says the PC at the address is still the one met before. mDNS results
// keep the add, update and lost flow of a working network untouched.
class Remembered(
    private val known: KnownDevices,
    private val devices: Devices,
    private val everyMs: Long = REMEMBERED_INTERVAL_MS,
) {
    // Missed probes in a row per device id. Three: about 45 s of a PC being gone, not one
    // lost Wi-Fi packet being read as a machine that went away.
    private val misses = HashMap<String, Int>()

    fun start() {
        thread(isDaemon = true, name = "remembered") {
            while (true) {
                round()
                Thread.sleep(everyMs)
            }
        }
    }

    // round probes every remembered PC once, in its own thread each, because a PC that is
    // gone keeps a read busy for its whole timeout and one by one would be that much per
    // PC added together. Forget a PC and its misses go with it.
    internal fun round() {
        val pcs = known.remembered()
        synchronized(misses) { misses.keys.retainAll(pcs.keys) }
        val probes = pcs.map { (id, address) ->
            thread(isDaemon = true, name = "remembered ${id.take(8)}") { probe(id, address) }
        }
        probes.forEach { it.join() }
    }

    private fun probe(id: String, address: String) {
        // A PC that has already missed its three probes stays off the list, but keeps
        // being probed: when it answers again the card comes straight back.
        val dropped = synchronized(misses) { (misses[id] ?: 0) >= DROPPED_AFTER_MISSES }
        if (!dropped) devices.seen(Device(id, id.take(8), emptyList(), address))
        val doc = try {
            readDocument(address, id).doc
        } catch (e: Exception) {
            val count = synchronized(misses) {
                misses[id] = (misses[id] ?: 0) + 1
                misses[id]!!
            }
            if (count >= DROPPED_AFTER_MISSES) devices.lost(id)
            return
        }
        synchronized(misses) { misses.remove(id) }
        known.remember(id, address)
        // The card keeps whatever firstContact the screen already shows it: the PC was
        // met before, and the document read here is not the moment to call it new.
        val current = devices.found.firstOrNull { it.id == id }
        devices.seen(
            Device(id, doc.name, doc.apps, address, confirmed = true, firstContact = current?.firstContact ?: false),
        )
    }

    companion object {
        private const val REMEMBERED_INTERVAL_MS = 15_000L
        private const val DROPPED_AFTER_MISSES = 3
    }
}

// forget puts a PC back to how it looked before this client had ever connected to it, so
// the fingerprint is offered for comparing again. The row stays where it is. Dropping it
// and waiting for the browse to bring it back works on the desktop, which asks the network
// every two seconds, and not on Android, where the resolver hands a PC over once and says
// nothing more about it until it leaves the network.
fun forget(device: Device, devices: Devices, known: KnownDevices) {
    known.forget(device.id)
    devices.seen(device.copy(firstContact = true))
}
