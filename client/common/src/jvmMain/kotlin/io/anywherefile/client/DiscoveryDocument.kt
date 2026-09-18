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
// bytes. The key and the proof it also carries are #127's.
@Serializable
data class DiscoveryDocument(
    val v: Int,
    @SerialName("device_id") val deviceId: String,
    val name: String,
    val apps: List<String> = emptyList(),
)

private val json = Json { ignoreUnknownKeys = true }

// The gateway's certificate is signed by the device key and by no authority, so the
// platform's trust store refuses it outright. Until #127 checks that signature and pins
// the key, this context accepts whatever certificate the PC serves, and the document is
// used for names only: nothing is sent and nothing here is trusted for access.
private val lanTls: SSLContext = SSLContext.getInstance("TLS").apply {
    val acceptAll = object : X509TrustManager {
        override fun checkClientTrusted(chain: Array<X509Certificate>, authType: String) {}
        override fun checkServerTrusted(chain: Array<X509Certificate>, authType: String) {}
        override fun getAcceptedIssuers(): Array<X509Certificate> = arrayOf()
    }
    init(null, arrayOf(acceptAll), null)
}

fun readDiscoveryDocument(address: String): DiscoveryDocument {
    val conn = URI("https://$address/.well-known/anywhere-file").toURL().openConnection() as HttpsURLConnection
    conn.sslSocketFactory = lanTls.socketFactory
    conn.setHostnameVerifier { _, _ -> true }
    conn.connectTimeout = 3_000
    conn.readTimeout = 3_000
    conn.inputStream.use { return json.decodeFromString(DiscoveryDocument.serializer(), it.readBytes().decodeToString()) }
}

// confirm reads the document for a PC the browse found and replaces what the record said.
// A document naming another device is somebody else's, or a copy, and changes nothing.
fun confirm(device: Device, devices: Devices) {
    thread(isDaemon = true, name = "discovery-document ${device.id.take(8)}") {
        val doc = try {
            readDiscoveryDocument(device.address)
        } catch (e: Exception) {
            return@thread
        }
        if (doc.deviceId == device.id) {
            devices.seen(device.copy(name = doc.name, apps = doc.apps, confirmed = true))
        }
    }
}
