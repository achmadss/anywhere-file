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

private val json = Json { ignoreUnknownKeys = true }

// No authority signs the gateway's certificate, so the platform's trust store has nothing
// to check it against and the handshake has to accept it as it stands. The check happens
// straight after, against the key in the document the connection just carried, which is the
// only order it can happen in: the document is what says which key to expect. Nothing is
// sent up before it passes. The request is a GET of a document the PC serves to anyone, and
// the answer is thrown away unless the signature holds.
private val lanTls: SSLContext = SSLContext.getInstance("TLS").apply {
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
fun readDiscoveryDocument(address: String, deviceId: String): DiscoveryDocument {
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
            ?: throw WrongDevice("This PC served no certificate.")
        verifyDeviceProof(deviceId, doc.publicKey, doc.tlsProof, certificate)
        return doc
    }
}

// confirm reads the document for a PC the browse found and replaces what the record said.
// A PC that cannot prove it holds the device key stays on the list with the reason, because
// a PC that has quietly turned into another one is worth seeing.
fun confirm(device: Device, devices: Devices, known: KnownDevices) {
    thread(isDaemon = true, name = "discovery-document ${device.id.take(8)}") {
        val doc = try {
            readDiscoveryDocument(device.address, device.id)
        } catch (e: WrongDevice) {
            devices.seen(device.copy(refused = e.message))
            return@thread
        } catch (e: Exception) {
            return@thread
        }
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
