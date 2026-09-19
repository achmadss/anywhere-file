package io.anywherefile.client

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import java.io.InputStream
import java.net.URI
import java.net.URLEncoder
import java.security.cert.X509Certificate
import java.time.Instant
import java.time.ZoneId
import java.time.format.DateTimeFormatter
import java.time.format.FormatStyle
import javax.net.ssl.HttpsURLConnection
import kotlin.concurrent.thread

// The file browser's half of the wire (#155). Every request is written down in
// client/dufs.http with what it answered.

// What the browser cannot do for itself, because each platform keeps its files somewhere
// else and asks for one its own way.
interface Transfers {
    // The file the person chose to send, or null when they chose nothing.
    suspend fun pick(): Outgoing?

    // Puts a file this client has just pulled down where downloads go on this platform,
    // and says where that was.
    fun save(name: String, body: InputStream): String
}

// A file on its way to a PC. length is -1 when the platform will not say how big it is.
class Outgoing(val name: String, val length: Long, val open: () -> InputStream)

// dufs's listing, the fields the screen uses. client/dufs.http has a whole one.
@Serializable
private class DufsIndex(
    val paths: List<DufsPath> = emptyList(),
    @SerialName("dir_exists") val dirExists: Boolean = true,
    @SerialName("allow_upload") val allowUpload: Boolean = false,
    @SerialName("allow_delete") val allowDelete: Boolean = false,
)

@Serializable
private class DufsPath(
    @SerialName("path_type") val pathType: String = "File",
    val name: String = "",
    val mtime: Long = 0,
    val size: Long = 0,
)

private val json = Json { ignoreUnknownKeys = true }

private val day = DateTimeFormatter.ofLocalizedDate(FormatStyle.MEDIUM).withZone(ZoneId.systemDefault())

class DufsFiles(
    private val address: String,
    override val app: String,
    // The public key of the certificate this PC proved is its own, from the discovery
    // document read before this was built. Every connection below has to carry it (#127).
    private val certificateKey: ByteArray,
    private val transfers: Transfers,
) : Files {

    override suspend fun list(dir: String): Listing {
        val conn = open(url(dir, query = "?json"), "GET")
        val body = conn.answer("read $dir").use { it.readBytes().decodeToString() }
        val index = json.decodeFromString(DufsIndex.serializer(), body)
        if (!index.dirExists) throw IllegalStateException("That folder is not on the PC any more.")
        val entries = index.paths
            .map { Entry(it.name, it.pathType == "Dir", it.size, day.format(Instant.ofEpochMilli(it.mtime))) }
            // Folders first and then by name, the way every file list a person has used
            // is ordered. dufs answers in its own order.
            .sortedWith(compareByDescending<Entry> { it.directory }.thenBy { it.name.lowercase() })
        return Listing(entries, index.allowUpload, index.allowDelete)
    }

    override suspend fun delete(dir: String, entry: Entry) {
        val conn = open(url(dir, entry.name), "DELETE")
        conn.answer("delete ${entry.name}").close()
    }

    override suspend fun download(dir: String, entry: Entry): String {
        val conn = open(url(dir, entry.name), "GET")
        return conn.answer("download ${entry.name}").use { transfers.save(entry.name, it) }
    }

    override suspend fun upload(dir: String): String? {
        val file = transfers.pick() ?: return null
        val conn = open(url(dir, file.name), "PUT")
        conn.doOutput = true
        // Streamed either way, so a file bigger than this process's memory still goes.
        if (file.length >= 0) conn.setFixedLengthStreamingMode(file.length) else conn.setChunkedStreamingMode(0)
        conn.pin()
        file.open().use { from -> conn.outputStream.use { from.copyTo(it) } }
        conn.answer("send ${file.name}").close()
        return "Sent ${file.name}"
    }

    // url builds the address of one thing on the PC. Each segment is encoded on its own:
    // a name can hold a space or an accent, and the listing hands it over decoded.
    private fun url(dir: String, name: String = "", query: String = ""): URI {
        val path = (dir + name).split('/').filter { it.isNotEmpty() }.joinToString("/") {
            URLEncoder.encode(it, "UTF-8").replace("+", "%20")
        }
        // A directory is asked for with the slash on the end. Without it dufs answers a
        // redirect instead of the listing.
        val slash = if (name.isEmpty()) "/" else ""
        return URI("https://$address/$app/$path$slash$query")
    }

    private fun open(url: URI, method: String): HttpsURLConnection {
        val conn = url.toURL().openConnection() as HttpsURLConnection
        conn.sslSocketFactory = lanTls.socketFactory
        // The certificate names the device under a suffix that resolves nowhere, and the
        // address came from mDNS. pin below is what the name would have been for.
        conn.setHostnameVerifier { _, _ -> true }
        conn.requestMethod = method
        conn.connectTimeout = 5_000
        return conn
    }

    // pin refuses a PC answering with a key other than the one it proved. connect() is
    // where the handshake happens, so this runs before a single byte of an upload has
    // left the process.
    private fun HttpsURLConnection.pin() {
        connect()
        val certificate = serverCertificates.firstOrNull() as? X509Certificate
        if (certificate == null || !certificate.publicKey.encoded.contentEquals(certificateKey)) {
            disconnect()
            throw WrongDevice("This PC is answering with a different key than the one it proved.")
        }
    }

    // answer pins the connection, checks the PC did what was asked, and hands back the
    // body. The caller closes it.
    private fun HttpsURLConnection.answer(what: String): InputStream {
        pin()
        if (responseCode !in 200..299) {
            val why = errorStream?.use { it.readBytes().decodeToString() }.orEmpty().trim().take(200)
            disconnect()
            throw IllegalStateException("The PC would not $what ($responseCode${if (why.isEmpty()) "" else ": $why"}).")
        }
        return inputStream
    }
}

// openFiles puts one of a PC's applications in front of the person. The discovery document
// is read first: that read is what proves which certificate key everything after it has to
// carry, and it is also the check that the PC is still the one the list was drawn from. It
// is a network round trip, so it happens off whichever thread the tap arrived on.
fun openFiles(device: Device, app: String, devices: Devices, transfers: Transfers, show: (Files) -> Unit) {
    thread(isDaemon = true, name = "open ${device.id.take(8)} $app") {
        val contact = try {
            readDiscoveryDocument(device.address, device.id)
        } catch (e: WrongDevice) {
            devices.seen(device.copy(refused = e.message))
            return@thread
        } catch (e: Exception) {
            devices.seen(device.copy(refused = "This PC did not answer."))
            return@thread
        }
        devices.seen(device.copy(refused = null))
        show(DufsFiles(device.address, app, contact.certificateKey, transfers))
    }
}
