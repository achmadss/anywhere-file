package io.anywherefile.client

import java.io.IOException
import java.net.InetAddress
import java.net.ServerSocket
import java.net.Socket
import java.security.cert.X509Certificate
import javax.net.ssl.SSLSocket
import kotlin.concurrent.thread

// Opening an application (#99). The gateway speaks HTTPS with a certificate no authority
// signs, so a browser has nothing to check it against, and the check this client does
// (#127) is not something a browser can be handed. So the client sits in the middle: it
// listens on loopback, and pipes each connection the browser makes over a TLS connection
// to the PC, requiring the certificate key the discovery document proved.
//
// Nothing here reads the HTTP going through. The gateway routes on the path and ignores
// the Host header (device/agent/gateway.go), so the bytes cross as they are, and whatever
// links the application writes point back at the loopback address, which is where the
// browser already is. It also means an upload or a download is a copy between two sockets
// and never a file in this process.
// Written out rather than taken from InetAddress.getLoopbackAddress, which answers ::1 on
// Android and leaves the listener bound where the browser is not looking: Chrome asked for
// 127.0.0.1 and was refused.
private const val LOOPBACK = "127.0.0.1"

class AppRelay(val address: String, private val certificateKey: ByteArray) {
    private val listener = ServerSocket(0, 50, InetAddress.getByName(LOOPBACK))

    val port: Int get() = listener.localPort

    init {
        thread(isDaemon = true, name = "app-relay $address") {
            while (true) {
                val browser = try {
                    listener.accept()
                } catch (e: IOException) {
                    return@thread // closed
                }
                thread(isDaemon = true, name = "app-relay $address connection") { carry(browser) }
            }
        }
    }

    fun url(app: String) = "http://$LOOPBACK:$port/$app/"

    fun close() = listener.close()

    private fun carry(browser: Socket) {
        browser.use {
            val pc = try {
                connect()
            } catch (e: Exception) {
                return
            }
            pc.use {
                // Both ways at once, because the browser is still sending a body while the
                // PC is already answering.
                val back = thread(isDaemon = true, name = "app-relay $address back") { copy(pc, browser) }
                copy(browser, pc)
                back.join()
            }
        }
    }

    // connect opens one TLS connection to the PC and refuses it unless the certificate
    // carries the key the document proved. The handshake itself accepts anything, for the
    // same reason it does when the document is read: no authority signed this.
    private fun connect(): SSLSocket {
        val colon = address.lastIndexOf(':')
        val host = address.substring(0, colon).removeSurrounding("[", "]")
        val socket = lanTls.socketFactory.createSocket(host, address.substring(colon + 1).toInt()) as SSLSocket
        try {
            socket.startHandshake()
            val certificate = socket.session.peerCertificates.firstOrNull() as? X509Certificate
            require(certificate != null && certificate.publicKey.encoded.contentEquals(certificateKey)) {
                "this PC is answering with a different key than the one it proved"
            }
        } catch (e: Exception) {
            socket.close()
            throw e
        }
        return socket
    }

    private fun copy(from: Socket, to: Socket) {
        try {
            from.getInputStream().copyTo(to.getOutputStream())
            to.shutdownOutput()
        } catch (e: IOException) {
            // One end went away. The other is closed by carry.
        }
    }
}

// One relay per PC, kept for as long as the app runs, because a browser opens several
// connections per page and comes back for every click.
private val relays = HashMap<String, AppRelay>()

// relayFor gives the relay for a PC, starting one if there is none. It reads the discovery
// document first: that read is what proves which certificate key to require, and it is
// also the check that the PC is still the one it was when the list was drawn.
fun relayFor(device: Device): AppRelay = synchronized(relays) {
    val open = relays[device.id]
    if (open != null && open.address == device.address) return open
    open?.close()
    val contact = readDiscoveryDocument(device.address, device.id)
    return AppRelay(device.address, contact.certificateKey).also { relays[device.id] = it }
}

// openApp puts one of a PC's applications in front of the person. Reading the document
// takes a network round trip, so it happens off whichever thread the tap arrived on, and
// a PC that has turned into another one lands on the row as a refusal instead of a browser
// window.
fun openApp(device: Device, app: String, devices: Devices, browser: (String) -> Unit) {
    thread(isDaemon = true, name = "open ${device.id.take(8)} $app") {
        val relay = try {
            relayFor(device)
        } catch (e: WrongDevice) {
            devices.seen(device.copy(refused = e.message))
            return@thread
        } catch (e: Exception) {
            devices.seen(device.copy(refused = "This PC did not answer."))
            return@thread
        }
        devices.seen(device.copy(refused = null))
        browser(relay.url(app))
    }
}
