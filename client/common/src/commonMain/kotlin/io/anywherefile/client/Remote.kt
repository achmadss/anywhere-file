package io.anywherefile.client

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.automirrored.filled.KeyboardArrowRight
import androidx.compose.material.icons.filled.Computer
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.FilterChip
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
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
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

// The PCs an account reaches through the server, and what an admin does with one (#102): see
// who may reach it, remove someone, and invite someone with a code. A guest sees the PC and
// what it shares, and none of the rest.

// The account's PCs, under the account card. It asks the server each time it comes on the
// screen, which is what makes a PC somebody was removed from go away.
@Composable
fun RemoteDevices(session: Account, onManage: (RemoteDevice) -> Unit) {
    if (!session.signedIn) return
    val scope = rememberCoroutineScope()
    var trouble by remember { mutableStateOf<String?>(null) }
    fun refresh() = scope.launch {
        trouble = try {
            withContext(Dispatchers.Default) { session.refresh() }
            null
        } catch (e: Exception) {
            e.message ?: "The server did not answer."
        }
    }
    LaunchedEffect(Unit) { refresh() }
    Column(verticalArrangement = Arrangement.spacedBy(12.dp)) {
        val listed = session.remote
        when {
            listed == null && trouble == null -> Text("Asking the server…", style = MaterialTheme.typography.bodyMedium)
            listed?.isEmpty() == true -> Text(
                "No devices on this account yet. Sign a PC in from its own settings, or join one with a code.",
                style = MaterialTheme.typography.bodyMedium,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
        }
        listed?.forEach { RemoteCard(it, onManage) }
        trouble?.let { Text(it, style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.error) }
        TextButton(onClick = { refresh() }) { Text("Refresh") }
        Redeem(session) { refresh() }
    }
}

// One PC as the server lists it. Only an admin's card opens anything, because the screen
// behind it is management and a guest has none.
@Composable
private fun RemoteCard(device: RemoteDevice, onManage: (RemoteDevice) -> Unit) {
    val admin = device.role == "admin"
    Card(
        Modifier.fillMaxWidth().then(if (admin) Modifier.clickable { onManage(device) } else Modifier),
        colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surfaceContainerHigh),
    ) {
        Row(Modifier.padding(16.dp), verticalAlignment = Alignment.CenterVertically) {
            Icon(Icons.Filled.Computer, null, Modifier.size(40.dp).padding(end = 8.dp))
            Column(Modifier.weight(1f).padding(start = 8.dp)) {
                Text(device.name, style = MaterialTheme.typography.titleMedium, maxLines = 1, overflow = TextOverflow.Ellipsis)
                Text(
                    (if (device.online) "Online" else "Offline") + " · " + (if (admin) "You manage it" else "Guest"),
                    style = MaterialTheme.typography.bodyMedium,
                )
                Text(
                    if (device.apps.isEmpty()) "Nothing shared yet" else "Shares " + device.apps.joinToString(", "),
                    style = MaterialTheme.typography.bodySmall,
                    maxLines = 2,
                    overflow = TextOverflow.Ellipsis,
                )
            }
            if (admin) Icon(Icons.AutoMirrored.Filled.KeyboardArrowRight, "Manage ${device.name}")
        }
    }
}

// Joining somebody's PC with the code they sent. It joins with whatever role the code was
// made for, which is guest unless its maker chose otherwise.
@Composable
private fun Redeem(session: Account, onJoined: () -> Unit) {
    var code by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    var trouble by remember { mutableStateOf<String?>(null) }
    val scope = rememberCoroutineScope()
    Card(
        Modifier.fillMaxWidth(),
        colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surfaceContainerLow),
    ) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
            Text("Got a code from someone? It lets you reach their PC.", style = MaterialTheme.typography.bodyMedium)
            OutlinedTextField(
                value = code,
                onValueChange = { code = it },
                label = { Text("Invitation code") },
                singleLine = true,
                modifier = Modifier.fillMaxWidth(),
            )
            if (busy) LinearProgressIndicator(Modifier.fillMaxWidth())
            trouble?.let { Text(it, style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.error) }
            Button(
                onClick = {
                    busy = true
                    trouble = null
                    scope.launch {
                        try {
                            withContext(Dispatchers.Default) { session.redeem(code) }
                            code = ""
                            onJoined()
                        } catch (e: Exception) {
                            trouble = e.message ?: "That did not work."
                        }
                        busy = false
                    }
                },
                enabled = !busy && code.isNotBlank(),
            ) { Text("Join") }
        }
    }
}

// How long a new code works. The server takes a duration and stops at a week.
private val EXPIRIES = listOf("1h" to "1 hour", "24h" to "1 day", "168h" to "7 days")

// The screen behind an admin's card: who may reach this PC, and a way to invite one more.
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun ManageDevice(session: Account, device: RemoteDevice, onLeave: () -> Unit) {
    var users by remember { mutableStateOf<List<DeviceUser>?>(null) }
    var trouble by remember { mutableStateOf<String?>(null) }
    var busy by remember { mutableStateOf(false) }
    var removing by remember { mutableStateOf<DeviceUser?>(null) }
    var role by remember { mutableStateOf("guest") }
    var expiry by remember { mutableStateOf("24h") }
    var made by remember { mutableStateOf<Invitation?>(null) }
    var shared by remember { mutableStateOf<String?>(null) }
    val share = rememberShare()
    val scope = rememberCoroutineScope()
    SystemBack(onLeave)

    fun carry(what: suspend CoroutineScope.() -> Unit) {
        if (busy) return
        busy = true
        trouble = null
        scope.launch {
            try {
                withContext(Dispatchers.Default) { what() }
            } catch (e: Exception) {
                trouble = e.message ?: "That did not work."
            }
            busy = false
        }
    }
    LaunchedEffect(Unit) { carry { users = session.users(device.id) } }

    removing?.let { user ->
        AlertDialog(
            onDismissRequest = { removing = null },
            title = { Text("Remove ${user.email}?") },
            text = { Text("They lose access to ${device.name} straight away. A new code brings them back.") },
            confirmButton = {
                TextButton(onClick = {
                    removing = null
                    carry {
                        session.revoke(device.id, user.id)
                        users = session.users(device.id)
                    }
                }) { Text("Remove") }
            },
            dismissButton = { TextButton(onClick = { removing = null }) { Text("Cancel") } },
        )
    }

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text(device.name, maxLines = 1, overflow = TextOverflow.Ellipsis) },
                navigationIcon = {
                    IconButton(onClick = onLeave) { Icon(Icons.AutoMirrored.Filled.ArrowBack, "Back to your devices") }
                },
                colors = TopAppBarDefaults.topAppBarColors(containerColor = MaterialTheme.colorScheme.surfaceContainer),
            )
        },
    ) { padding ->
        Column(
            Modifier.padding(padding).safeDrawingPadding().fillMaxSize().verticalScroll(rememberScrollState()).padding(24.dp),
            verticalArrangement = Arrangement.spacedBy(16.dp),
        ) {
            if (busy) LinearProgressIndicator(Modifier.fillMaxWidth())
            trouble?.let { Text(it, style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.error) }

            Text("Who can reach it", style = MaterialTheme.typography.titleMedium)
            users?.forEach { user ->
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Column(Modifier.weight(1f)) {
                        Text(user.email, style = MaterialTheme.typography.bodyLarge, maxLines = 1, overflow = TextOverflow.Ellipsis)
                        Text(
                            if (user.role == "admin") "Manages it" else "Guest",
                            style = MaterialTheme.typography.bodySmall,
                            color = MaterialTheme.colorScheme.onSurfaceVariant,
                        )
                    }
                    // Removing yourself from the screen that is managing the PC is a way to
                    // lock yourself out by accident, so it is not offered here.
                    if (user.email == session.email) {
                        Text("You", style = MaterialTheme.typography.labelLarge)
                    } else {
                        TextButton(onClick = { removing = user }, enabled = !busy) { Text("Remove") }
                    }
                }
            }

            Text("Invite someone", style = MaterialTheme.typography.titleMedium)
            Text("They join as", style = MaterialTheme.typography.bodyMedium)
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                FilterChip(selected = role == "guest", onClick = { role = "guest" }, label = { Text("Guest") })
                FilterChip(selected = role == "admin", onClick = { role = "admin" }, label = { Text("Admin") })
            }
            Text("The code works once, for", style = MaterialTheme.typography.bodyMedium)
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                EXPIRIES.forEach { (value, words) ->
                    FilterChip(selected = expiry == value, onClick = { expiry = value }, label = { Text(words) })
                }
            }
            Button(
                onClick = {
                    shared = null
                    carry { made = session.invite(device.id, role, expiry) }
                },
                enabled = !busy,
            ) { Text("Make a code") }

            made?.let { invitation ->
                Card(
                    Modifier.fillMaxWidth(),
                    colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.primaryContainer),
                ) {
                    Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                        SelectionContainer {
                            Text(invitation.code, style = MaterialTheme.typography.titleMedium, fontFamily = FontFamily.Monospace)
                        }
                        Text(
                            "Works once, until ${invitation.until}. It is shown only now.",
                            style = MaterialTheme.typography.bodySmall,
                        )
                        Button(onClick = { shared = share(message(session.server, device.name, invitation)) }) {
                            Text("Share")
                        }
                        shared?.let { Text(it, style = MaterialTheme.typography.bodySmall) }
                    }
                }
            }
        }
    }
}

// What the person receiving a code reads. It names the server, because a code only works on
// the one it was made on.
private fun message(server: String, device: String, invitation: Invitation) =
    "You're invited to reach $device with anywhere-file. Sign in at $server in the app, " +
        "then enter this code under \"Got a code from someone?\": ${invitation.code}\n" +
        "It works once, until ${invitation.until}."
