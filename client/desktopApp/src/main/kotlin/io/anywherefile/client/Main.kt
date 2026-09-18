package io.anywherefile.client

import androidx.compose.ui.window.Window
import androidx.compose.ui.window.application

fun main() {
    val devices = Devices()
    val discovery = JmdnsDiscovery(devices)
    discovery.start()
    application {
        Window(onCloseRequest = ::exitApplication, title = "anywhere-file") {
            DeviceList(devices)
        }
    }
}
