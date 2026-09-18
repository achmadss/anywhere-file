package io.anywherefile.client

import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.delay

// The browse deadline. docs/spikes/mdns.md measured answers at roughly 20 ms or roughly
// 1 s, because the first retry is at 1 s. Five seconds covers a lost query more than
// once, so until then an empty list reads as still looking, not as nothing here.
const val BROWSE_DEADLINE_MS = 5_000L

@Composable
fun DeviceList(devices: Devices) {
    var pastDeadline by remember { mutableStateOf(false) }
    LaunchedEffect(Unit) {
        delay(BROWSE_DEADLINE_MS)
        pastDeadline = true
    }
    MaterialTheme {
        Column(Modifier.fillMaxSize().padding(24.dp)) {
            Text("PCs on this network", style = MaterialTheme.typography.titleLarge)
            Spacer(Modifier.height(16.dp))
            when {
                devices.found.isNotEmpty() -> LazyColumn {
                    items(devices.found, key = { it.id }) { DeviceRow(it) }
                }
                pastDeadline -> Text(
                    "None found. A PC shows up here when the agent is running on it and it is on the same network as this device.",
                    style = MaterialTheme.typography.bodyMedium,
                )
                else -> Text("Looking…", style = MaterialTheme.typography.bodyMedium)
            }
        }
    }
}

@Composable
private fun DeviceRow(device: Device) {
    Column(Modifier.padding(vertical = 8.dp)) {
        Text(device.name, style = MaterialTheme.typography.titleMedium)
        Text(
            if (device.apps.isEmpty()) "shares nothing yet" else device.apps.joinToString(", "),
            style = MaterialTheme.typography.bodyMedium,
        )
        Text(device.address, style = MaterialTheme.typography.bodySmall)
    }
}
