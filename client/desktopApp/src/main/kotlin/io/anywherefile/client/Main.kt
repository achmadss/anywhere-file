package io.anywherefile.client

import androidx.compose.ui.window.Window
import androidx.compose.ui.window.application
import java.awt.Desktop
import java.io.File
import java.net.URI

fun main() {
    val devices = Devices()
    val known = KnownDevices(File(stateDir(), "known-devices"))
    val discovery = LanDiscovery(devices, known)
    discovery.start()
    application {
        Window(onCloseRequest = ::exitApplication, title = "anywhere-file") {
            DeviceList(
                devices,
                onForget = { forget(it, devices, known) },
                onOpen = { device, app -> openApp(device, app, devices, ::browse) },
            )
        }
    }
}

// The system browser. Desktop.browse is the portable way and is missing on a Linux box
// with no desktop session, where xdg-open is what every other program falls back to.
private fun browse(url: String) {
    val desktop = Desktop.getDesktop().takeIf { Desktop.isDesktopSupported() && it.isSupported(Desktop.Action.BROWSE) }
    if (desktop != null) desktop.browse(URI(url)) else ProcessBuilder("xdg-open", url).start()
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
