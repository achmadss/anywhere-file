package io.anywherefile.client

import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.window.Window
import androidx.compose.ui.window.application
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import java.awt.EventQueue
import java.awt.FileDialog
import java.awt.Frame
import java.io.File
import java.io.InputStream

fun main() {
    val devices = Devices()
    val state = stateDir()
    val known = KnownDevices(File(state, "known-devices"))
    val discovery = LanDiscovery(devices, known)
    discovery.start()
    val transfers = DesktopTransfers()
    // The account and where its token is kept (#100): the login keychain where this system
    // has one, and a file only this account can read where it does not.
    val session = Session(DesktopSessions(File(state, "session")), ServerAddress(File(state, "server")))
    application {
        Window(onCloseRequest = ::exitApplication, title = "anywhere-file") {
            // The folder the person is looking at, or null for the home screen.
            var browsing by remember { mutableStateOf<Files?>(null) }
            // True while the sign-in screen is up (#100).
            var signingIn by remember { mutableStateOf(false) }
            AnywhereFile {
                val files = browsing
                when {
                    files != null -> FileBrowser(files) { browsing = null }
                    signingIn -> SignIn(session) { signingIn = false }
                    else -> Home(devices, session, onSignIn = { signingIn = true }) { device, app ->
                        openFiles(device, app, devices, transfers) { browsing = it }
                    }
                }
            }
        }
    }
}

// Where a file comes from and where one lands, on a desktop (#155).
private class DesktopTransfers : Transfers {
    override suspend fun pick(): Outgoing? = withContext(Dispatchers.IO) {
        val dialog = FileDialog(null as Frame?, "Send a file", FileDialog.LOAD)
        // The dialog belongs to the toolkit's own thread, and showing it there blocks
        // until the person is done with it, which is what this call is waiting for.
        EventQueue.invokeAndWait { dialog.isVisible = true }
        val dir = dialog.directory
        val name = dialog.file
        if (dir == null || name == null) return@withContext null
        val file = File(dir, name)
        Outgoing(file.name, file.length()) { file.inputStream() }
    }

    override fun save(name: String, body: InputStream): String {
        val home = File(System.getProperty("user.home"))
        val downloads = File(home, "Downloads").takeIf { it.isDirectory } ?: home
        val file = free(downloads, name)
        file.outputStream().use { body.copyTo(it) }
        return "Saved to ${file.path}"
    }
}

// A download does not write over a file that is already there. Windows, macOS and every
// browser do the same thing with the same brackets.
private fun free(dir: File, name: String): File {
    if (!File(dir, name).exists()) return File(dir, name)
    val stem = name.substringBeforeLast('.', name)
    val extension = name.substringAfterLast('.', "")
    var n = 2
    while (true) {
        val next = File(dir, if (extension.isEmpty()) "$stem ($n)" else "$stem ($n).$extension")
        if (!next.exists()) return next
        n++
    }
}

// Where this client keeps what it has learned, next to where the agent keeps its own on
// the same PC (device/agent/config.go). On Windows that is the local directory rather than
// the roaming one, because what a client knows about a network belongs to the machine.
private fun stateDir(): File {
    val home = File(System.getProperty("user.home"))
    val os = System.getProperty("os.name").lowercase()
    val base = when {
        os.startsWith("windows") -> System.getenv("LOCALAPPDATA")?.let(::File) ?: File(home, "AppData/Local")
        os.startsWith("mac") -> File(home, "Library/Application Support")
        else -> System.getenv("XDG_CONFIG_HOME")?.let(::File) ?: File(home, ".config")
    }
    return File(base, "anywhere-file")
}
