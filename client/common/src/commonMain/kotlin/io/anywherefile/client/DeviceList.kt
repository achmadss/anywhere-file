package io.anywherefile.client

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.AssistChip
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedCard
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.material3.TopAppBar
import androidx.compose.material3.TopAppBarDefaults
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.delay

// The browse deadline. docs/spikes/mdns.md measured answers at roughly 20 ms or roughly
// 1 s, because the first retry is at 1 s. Five seconds covers a lost query more than
// once, so until then an empty list reads as still looking, not as nothing here.
const val BROWSE_DEADLINE_MS = 5_000L

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun DeviceList(devices: Devices, onForget: (Device) -> Unit = {}, onOpen: (Device, String) -> Unit = { _, _ -> }) {
    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text("PCs on this network") },
                colors = TopAppBarDefaults.topAppBarColors(
                    containerColor = MaterialTheme.colorScheme.surfaceContainer,
                ),
            )
        },
    ) { padding ->
        DeviceRows(devices, onForget, onOpen, Modifier.padding(padding))
    }
}

// The rows on their own, so the screen that asks for the local network permission can show
// whatever has turned up underneath its own explanation.
@Composable
fun DeviceRows(
    devices: Devices,
    onForget: (Device) -> Unit = {},
    onOpen: (Device, String) -> Unit = { _, _ -> },
    modifier: Modifier = Modifier,
) {
    var pastDeadline by remember { mutableStateOf(false) }
    LaunchedEffect(Unit) {
        delay(BROWSE_DEADLINE_MS)
        pastDeadline = true
    }
    when {
        devices.found.isNotEmpty() -> LazyColumn(
            modifier.fillMaxSize(),
            contentPadding = PaddingValues(16.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            items(devices.found, key = { it.id }) { DeviceCard(it, onForget, onOpen) }
        }
        pastDeadline -> Empty(
            modifier,
            "No PCs yet. One turns up here when the agent is running on it and it is on this network.",
        )
        else -> Looking(modifier)
    }
}

@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun DeviceCard(device: Device, onForget: (Device) -> Unit, onOpen: (Device, String) -> Unit) {
    OutlinedCard(Modifier.fillMaxWidth()) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Text(device.name, style = MaterialTheme.typography.titleMedium, maxLines = 1, overflow = TextOverflow.Ellipsis)
            when {
                device.refused != null -> Text(
                    device.refused,
                    style = MaterialTheme.typography.bodyMedium,
                    color = MaterialTheme.colorScheme.error,
                )
                device.apps.isEmpty() -> Text(
                    "Sharing nothing yet.",
                    style = MaterialTheme.typography.bodyMedium,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
                // One chip per shared folder. It opens here, in this app (#155).
                else -> FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    device.apps.forEach { app ->
                        AssistChip(onClick = { onOpen(device, app) }, label = { Text(app) })
                    }
                }
            }
            // The fingerprint is what `agent key` prints on the PC itself, so the two can
            // be held up against each other. That is worth doing the first time a PC turns
            // up, and forgetting a PC is how to be asked again.
            Text(
                "${device.address}\n${fingerprintOf(device.id)}",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            when {
                device.firstContact -> Text(
                    "New. Check this against the fingerprint the agent prints on the PC.",
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.primary,
                )
                device.confirmed -> Row { TextButton(onClick = { onForget(device) }) { Text("Forget") } }
            }
        }
    }
}

@Composable
private fun Empty(modifier: Modifier, what: String) {
    Box(modifier.fillMaxSize().padding(24.dp), Alignment.Center) {
        Text(
            what,
            style = MaterialTheme.typography.bodyLarge,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
        )
    }
}

@Composable
private fun Looking(modifier: Modifier) {
    Row(
        modifier.fillMaxSize().padding(24.dp),
        horizontalArrangement = Arrangement.spacedBy(12.dp, Alignment.CenterHorizontally),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        CircularProgressIndicator(Modifier.size(20.dp))
        Text("Looking…", style = MaterialTheme.typography.bodyLarge)
    }
}
