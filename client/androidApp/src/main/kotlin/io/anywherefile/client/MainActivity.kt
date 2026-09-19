package io.anywherefile.client

import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.material3.Button
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.SideEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import androidx.core.view.WindowCompat
import java.io.File

// The constant for this is in the API 37 SDK only, and a string is what the platform gets
// either way.
private const val ACCESS_LOCAL_NETWORK = "android.permission.ACCESS_LOCAL_NETWORK"

class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        // The platform title bar repeats the app name over the screen's own heading, and
        // from targetSdk 35 Android draws the window edge to edge, so it lands on top of
        // the content instead of above it.
        actionBar?.hide()
        val devices = Devices()
        val known = KnownDevices(File(filesDir, "known-devices"))
        val discovery = NsdDiscovery(this, devices, known)
        val onForget = { device: Device -> forget(device, devices, known) }
        val transfers = AndroidTransfers(applicationContext)
        setContent {
            val dark = isSystemInDarkTheme()
            // There is no bar behind the status icons, so they are drawn over whatever the
            // screen is and have to be the opposite of it to stay readable.
            SideEffect {
                WindowCompat.getInsetsController(window, window.decorView).isAppearanceLightStatusBars = !dark
            }
            var allowed by remember { mutableStateOf(localNetworkAllowed()) }
            var denied by remember { mutableStateOf(false) }
            // The folder the person is looking at, or null for the list of PCs.
            var browsing by remember { mutableStateOf<Files?>(null) }
            val ask = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
                allowed = granted
                denied = !granted
            }
            val pick = rememberLauncherForActivityResult(ActivityResultContracts.GetContent()) { transfers.chose(it) }
            SideEffect { transfers.ask = { pick.launch("*/*") } }
            val onOpen = { device: Device, app: String ->
                openFiles(device, app, devices, transfers) { browsing = it }
            }
            AnywhereFile {
                val files = browsing
                when {
                    files != null -> FileBrowser(files) { browsing = null }
                    allowed -> {
                        DisposableEffect(Unit) {
                            discovery.start()
                            onDispose { discovery.stop() }
                        }
                        DeviceList(devices, onForget, onOpen)
                    }
                    else -> LocalNetworkGate(
                        denied,
                        onAsk = { ask.launch(ACCESS_LOCAL_NETWORK) },
                        onPick = discovery::pick,
                        devices,
                        onForget,
                        onOpen,
                    )
                }
            }
        }
    }

    // Android 17 asks before an app may look around the local network. Earlier versions
    // let any app with INTERNET do it.
    private fun localNetworkAllowed() = Build.VERSION.SDK_INT < 37 ||
        checkSelfPermission(ACCESS_LOCAL_NETWORK) == PackageManager.PERMISSION_GRANTED
}

// The explanation comes before the prompt, and the system picker is the way in when the
// answer was no: Android shows the PCs it can see and hands over the one chosen.
@Composable
private fun LocalNetworkGate(
    denied: Boolean,
    onAsk: () -> Unit,
    onPick: () -> Unit,
    devices: Devices,
    onForget: (Device) -> Unit,
    onOpen: (Device, String) -> Unit,
) {
    Column(
        Modifier.safeDrawingPadding().fillMaxSize().padding(24.dp),
        verticalArrangement = Arrangement.spacedBy(16.dp),
    ) {
        Text("PCs on this network", style = MaterialTheme.typography.headlineSmall)
        Text(
            "anywhere-file finds your PCs by asking this network which of them run the agent. Android asks you before an app may do that.",
            style = MaterialTheme.typography.bodyMedium,
        )
        Button(onClick = onAsk) { Text("Allow") }
        if (denied) {
            Text(
                "Without that, Android can still hand over one PC at a time from its own list.",
                style = MaterialTheme.typography.bodyMedium,
            )
            Button(onClick = onPick) { Text("Pick a PC") }
        }
        if (devices.found.isNotEmpty()) DeviceRows(devices, onForget, onOpen)
    }
}
