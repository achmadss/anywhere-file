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
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.Computer
import androidx.compose.material.icons.filled.Folder
import androidx.compose.material.icons.filled.Warning
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.Icon
import androidx.compose.material3.LocalContentColor
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
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

// The first screen: the devices this one can reach, and what each of them shares (#162).
// A device is a card, and the folders it shares are the tiles inside that card, so opening
// a folder is one tap from the first thing on the screen.

// The browse deadline. docs/spikes/mdns.md measured answers at roughly 20 ms or roughly
// 1 s, because the first retry is at 1 s. Five seconds covers a lost query more than
// once, so until then an empty list reads as still looking, not as nothing here.
const val BROWSE_DEADLINE_MS = 5_000L

@Composable
fun Home(
    devices: Devices,
    session: Account,
    onSignIn: () -> Unit = {},
    onManage: (RemoteDevice) -> Unit = {},
    onOpen: (Device, String) -> Unit = { _, _ -> },
) {
    // The screen has no bar of its own, so the background is this. Without it the window
    // shows whatever it was born with, which is the wrong colour half the time.
    Surface(Modifier.fillMaxSize()) {
        LazyColumn(
            // Android draws the window edge to edge, so the status bar sits over whatever
            // is at the top until this pushes it down.
            Modifier.safeDrawingPadding(),
            contentPadding = PaddingValues(16.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            item {
                Text(
                    "anywhere-file",
                    style = MaterialTheme.typography.headlineMedium,
                    modifier = Modifier.padding(bottom = 8.dp),
                )
            }
            item { Heading("On this network") }
            deviceItems(devices, onOpen)
            item { Heading("Away from home") }
            item { AccountCard(session, onSignIn) }
            item { RemoteDevices(session, onManage) }
        }
    }
}

// The devices on their own, for the screen that has to explain the local network
// permission first and still wants to show whatever has turned up underneath.
@Composable
fun DeviceRows(
    devices: Devices,
    onOpen: (Device, String) -> Unit = { _, _ -> },
    modifier: Modifier = Modifier,
) {
    LazyColumn(modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(12.dp)) {
        deviceItems(devices, onOpen)
    }
}

// Whatever the list of devices is right now: the cards, or the one line that stands in for
// them while there are none.
private fun LazyListScope.deviceItems(
    devices: Devices,
    onOpen: (Device, String) -> Unit,
) {
    if (devices.found.isEmpty()) {
        item { Waiting() }
        return
    }
    items(devices.found, key = { it.id }) { DeviceCard(it, onOpen) }
}

@Composable
private fun Heading(text: String) {
    Text(
        text,
        style = MaterialTheme.typography.titleSmall,
        color = MaterialTheme.colorScheme.onSurfaceVariant,
        modifier = Modifier.padding(start = 4.dp, top = 8.dp),
    )
}

// A device, and the folders it shares. The card is filled with the accent colour once the
// device has proved who it is and has something to open, because that is the card meant to
// be tapped. One that cannot be opened yet stays quiet.
@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun DeviceCard(device: Device, onOpen: (Device, String) -> Unit) {
    val ready = device.refused == null && device.apps.isNotEmpty()
    Card(
        Modifier.fillMaxWidth(),
        colors = CardDefaults.cardColors(
            containerColor = when {
                device.refused != null -> MaterialTheme.colorScheme.errorContainer
                ready -> MaterialTheme.colorScheme.primaryContainer
                else -> MaterialTheme.colorScheme.surfaceContainerHigh
            },
        ),
    ) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Icon(
                    if (device.refused != null) Icons.Filled.Warning else Icons.Filled.Computer,
                    null,
                    Modifier.size(40.dp).padding(end = 8.dp),
                )
                Column(Modifier.padding(start = 8.dp)) {
                    Text(
                        device.name,
                        style = MaterialTheme.typography.titleMedium,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                    )
                    Text(
                        device.refused ?: shares(device),
                        style = MaterialTheme.typography.bodyMedium,
                        maxLines = 2,
                        overflow = TextOverflow.Ellipsis,
                    )
                }
            }
            if (device.apps.isNotEmpty() && device.refused == null) {
                FlowRow(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    device.apps.forEach { app ->
                        FolderTile(app) { onOpen(device, app) }
                    }
                }
            }
            // Two devices can carry the same name, and this is what tells them apart.
            Text(
                device.address,
                style = MaterialTheme.typography.bodySmall,
                color = LocalContentColor.current.copy(alpha = 0.7f),
            )
        }
    }
}

// One shared folder. It is the tile from the quick access row of the layout this screen
// follows: an icon, a name, and the whole thing is the target.
@Composable
private fun FolderTile(name: String, onOpen: () -> Unit) {
    Surface(
        onClick = onOpen,
        shape = MaterialTheme.shapes.medium,
        color = MaterialTheme.colorScheme.surface.copy(alpha = 0.5f),
    ) {
        Row(
            Modifier.padding(horizontal = 12.dp, vertical = 10.dp),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(8.dp),
        ) {
            Icon(Icons.Filled.Folder, null, Modifier.size(20.dp))
            Text(name, style = MaterialTheme.typography.labelLarge, maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
    }
}

// What the card says under the name. A device that has answered for itself lists its
// folders in the tiles below, so this line only has to say how many.
private fun shares(device: Device) = when {
    device.apps.size == 1 -> "1 shared folder"
    device.apps.isNotEmpty() -> "${device.apps.size} shared folders"
    device.confirmed -> "Nothing shared yet"
    else -> "Checking…"
}

// Looking, then giving up on looking. The deadline is the only thing that tells those two
// apart: a network with nothing on it and a network that has not answered yet look the same.
@Composable
private fun Waiting() {
    var pastDeadline by remember { mutableStateOf(false) }
    LaunchedEffect(Unit) {
        delay(BROWSE_DEADLINE_MS)
        pastDeadline = true
    }
    if (pastDeadline) {
        Empty("No devices yet. One shows up here when anywhere-file is running on it and it is on this network.")
    } else {
        Row(
            Modifier.fillMaxWidth().padding(vertical = 24.dp),
            horizontalArrangement = Arrangement.spacedBy(12.dp, Alignment.CenterHorizontally),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            CircularProgressIndicator(Modifier.size(20.dp))
            Text("Looking…", style = MaterialTheme.typography.bodyLarge)
        }
    }
}

@Composable
private fun Empty(what: String) {
    Box(Modifier.fillMaxWidth().padding(vertical = 24.dp), Alignment.Center) {
        Text(
            what,
            style = MaterialTheme.typography.bodyLarge,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
        )
    }
}
