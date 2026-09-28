package io.anywherefile.client

import com.sun.net.httpserver.HttpServer
import kotlinx.coroutines.runBlocking
import java.io.File
import java.net.InetSocketAddress
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull

// A session kept at start while the server was not answering learns whose it is at the next
// refresh. The manage screen tells your own row apart by that address, and without it offers
// to remove you.
class SessionRefreshTest {
    @Test
    fun aRefreshFillsInTheAddressResumeCouldNotCheck() {
        var up = false
        val server = HttpServer.create(InetSocketAddress("127.0.0.1", 0), 0)
        fun answer(path: String, body: String) = server.createContext(path) { exchange ->
            val (status, text) = if (up) 200 to body else 503 to """{"error":"down"}"""
            val bytes = text.encodeToByteArray()
            exchange.sendResponseHeaders(status, bytes.size.toLong())
            exchange.responseBody.use { it.write(bytes) }
        }
        answer("/v1/me", """{"email":"me@example.test"}""")
        answer("/v1/devices", """{"devices":[]}""")
        server.start()
        try {
            val address = File.createTempFile("server", "").apply { writeText("http://127.0.0.1:${server.address.port}\n") }
            val store = Held().apply { save("a-session-token") }
            val session = Session(store, ServerAddress(address))
            runBlocking {
                session.resume()
                assertEquals(true, session.signedIn, "a server that did not answer signed the session out")
                assertNull(session.email)

                up = true
                session.refresh()
                assertEquals("me@example.test", session.email)
            }
        } finally {
            server.stop(0)
        }
    }
}
