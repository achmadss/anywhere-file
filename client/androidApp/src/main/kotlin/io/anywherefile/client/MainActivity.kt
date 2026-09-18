package io.anywherefile.client

import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Button
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp

// The constant for this is in the API 37 SDK only, and a string is what the platform gets
// either way.
private const val ACCESS_LOCAL_NETWORK = "android.permission.ACCESS_LOCAL_NETWORK"

class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val devices = Devices()
        val discovery = NsdDiscovery(this, devices)
        setContent {
            var allowed by remember { mutableStateOf(localNetworkAllowed()) }
            var denied by remember { mutableStateOf(false) }
            val ask = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
                allowed = granted
                denied = !granted
            }
            if (allowed) {
                DisposableEffect(Unit) {
                    discovery.start()
                    onDispose { discovery.stop() }
                }
                DeviceList(devices)
            } else {
                LocalNetworkGate(denied, onAsk = { ask.launch(ACCESS_LOCAL_NETWORK) }, onPick = discovery::pick, devices)
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
private fun LocalNetworkGate(denied: Boolean, onAsk: () -> Unit, onPick: () -> Unit, devices: Devices) {
    MaterialTheme {
        Column(Modifier.padding(24.dp)) {
            Text("PCs on this network", style = MaterialTheme.typography.titleLarge)
            Spacer(Modifier.height(16.dp))
            Text(
                "anywhere-file finds your PCs by asking this network which of them run the agent. Android asks you before an app may do that.",
                style = MaterialTheme.typography.bodyMedium,
            )
            Spacer(Modifier.height(16.dp))
            Button(onClick = onAsk) { Text("Allow") }
            if (denied) {
                Spacer(Modifier.height(24.dp))
                Text(
                    "Without that, Android can still hand over one PC at a time from its own list.",
                    style = MaterialTheme.typography.bodyMedium,
                )
                Spacer(Modifier.height(8.dp))
                Button(onClick = onPick) { Text("Pick a PC") }
            }
            if (devices.found.isNotEmpty()) {
                Spacer(Modifier.height(24.dp))
                DeviceList(devices)
            }
        }
    }
}
