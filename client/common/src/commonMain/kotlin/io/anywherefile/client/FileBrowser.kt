package io.anywherefile.client

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.automirrored.filled.KeyboardArrowRight
import androidx.compose.material.icons.filled.Add
import androidx.compose.material.icons.filled.Delete
import androidx.compose.material.icons.filled.MoreVert
import androidx.compose.material.icons.filled.Refresh
import androidx.compose.material3.Button
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.ExtendedFloatingActionButton
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.IconButton
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.ListItem
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Scaffold
import androidx.compose.material3.SnackbarHost
import androidx.compose.material3.SnackbarHostState
import androidx.compose.material3.Text
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
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

// One PC's shared folder, drawn by this client (#155). dufs answers a listing as JSON and
// hands over the bytes on a plain GET, so there is nothing here a browser was needed for,
// and everything goes over the client's own pinned TLS to the gateway (#127). The requests
// are written down, with what each one answers, in client/dufs.http.

// One row. modified arrives already written the way the platform writes a date, because
// that is the only thing on this screen that needs a platform to do it.
data class Entry(val name: String, val directory: Boolean, val size: Long, val modified: String)

class Listing(val entries: List<Entry>, val canUpload: Boolean, val canDelete: Boolean)

// What the screen asks a PC for. DufsFiles is the one implementation.
interface Files {
    val app: String

    suspend fun list(dir: String): Listing

    suspend fun delete(dir: String, entry: Entry)

    // These two end in a line to put in front of the person: where the file landed, or
    // what was sent. upload answers null when nobody chose a file.
    suspend fun download(dir: String, entry: Entry): String

    suspend fun upload(dir: String): String?
}

// The directory above this one. "" is the top, and the top is where leaving happens.
fun parentOf(dir: String): String {
    val above = dir.trimEnd('/').substringBeforeLast('/', "")
    return if (above.isEmpty()) "" else "$above/"
}

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun FileBrowser(files: Files, onLeave: () -> Unit) {
    var dir by remember { mutableStateOf("") }
    var again by remember { mutableStateOf(0) }
    var listing by remember { mutableStateOf<Listing?>(null) }
    var failed by remember { mutableStateOf<String?>(null) }
    var busy by remember { mutableStateOf(false) }
    val said = remember { SnackbarHostState() }
    val scope = rememberCoroutineScope()

    val up = {
        if (dir.isEmpty()) onLeave() else dir = parentOf(dir)
    }
    SystemBack(up)

    LaunchedEffect(dir, again) {
        listing = null
        failed = null
        try {
            listing = withContext(Dispatchers.Default) { files.list(dir) }
        } catch (e: Exception) {
            failed = e.message ?: "That device did not answer."
        }
    }

    // One transfer at a time. A second tap while a big file is on its way is nobody's
    // intention, and the bar across the top is what says so.
    fun carry(what: suspend () -> String?) {
        if (busy) return
        busy = true
        scope.launch {
            val told = try {
                withContext(Dispatchers.Default) { what() }
            } catch (e: Exception) {
                e.message ?: "That did not work."
            }
            busy = false
            again++
            if (told != null) said.showSnackbar(told)
        }
    }

    Scaffold(
        topBar = {
            TopAppBar(
                title = {
                    Column {
                        Text(files.app, maxLines = 1, overflow = TextOverflow.Ellipsis)
                        if (dir.isNotEmpty()) {
                            Text(
                                dir.trimEnd('/'),
                                style = MaterialTheme.typography.labelMedium,
                                maxLines = 1,
                                overflow = TextOverflow.MiddleEllipsis,
                            )
                        }
                    }
                },
                navigationIcon = {
                    IconButton(onClick = up) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, if (dir.isEmpty()) "Back to your devices" else "Up a folder")
                    }
                },
                actions = {
                    IconButton(onClick = { again++ }) { Icon(Icons.Default.Refresh, "Refresh") }
                },
                colors = TopAppBarDefaults.topAppBarColors(
                    containerColor = MaterialTheme.colorScheme.surfaceContainer,
                ),
            )
        },
        floatingActionButton = {
            if (listing?.canUpload == true) {
                ExtendedFloatingActionButton(
                    onClick = { carry { files.upload(dir) } },
                    icon = { Icon(Icons.Default.Add, null) },
                    text = { Text("Send a file") },
                )
            }
        },
        snackbarHost = { SnackbarHost(said) },
    ) { padding ->
        Column(Modifier.padding(padding).fillMaxSize()) {
            if (busy) LinearProgressIndicator(Modifier.fillMaxWidth())
            val here = listing
            when {
                failed != null -> Trouble(failed!!) { again++ }
                here == null -> Box(Modifier.fillMaxSize(), Alignment.Center) { CircularProgressIndicator() }
                here.entries.isEmpty() -> Nothing(dir)
                else -> LazyColumn {
                    items(here.entries, key = { it.name }) { entry ->
                        EntryRow(
                            entry,
                            canDelete = here.canDelete,
                            onOpen = { dir = "$dir${entry.name}/" },
                            onDownload = { carry { files.download(dir, entry) } },
                            onDelete = { carry { files.delete(dir, entry); "Deleted ${entry.name}" } },
                        )
                        HorizontalDivider(color = MaterialTheme.colorScheme.outlineVariant)
                    }
                }
            }
        }
    }
}

@Composable
private fun EntryRow(
    entry: Entry,
    canDelete: Boolean,
    onOpen: () -> Unit,
    onDownload: () -> Unit,
    onDelete: () -> Unit,
) {
    var menu by remember { mutableStateOf(false) }
    ListItem(
        modifier = Modifier.clickable { if (entry.directory) onOpen() else onDownload() },
        headlineContent = { Text(entry.name, maxLines = 1, overflow = TextOverflow.MiddleEllipsis) },
        supportingContent = {
            Text(
                if (entry.directory) entry.modified else "${humanSize(entry.size)} · ${entry.modified}",
                style = MaterialTheme.typography.bodySmall,
            )
        },
        trailingContent = {
            if (entry.directory) {
                Icon(Icons.AutoMirrored.Filled.KeyboardArrowRight, null)
            } else {
                Box {
                    IconButton(onClick = { menu = true }) { Icon(Icons.Default.MoreVert, "More for ${entry.name}") }
                    DropdownMenu(expanded = menu, onDismissRequest = { menu = false }) {
                        DropdownMenuItem(
                            text = { Text("Save a copy here") },
                            onClick = { menu = false; onDownload() },
                        )
                        if (canDelete) {
                            DropdownMenuItem(
                                text = { Text("Delete from the device") },
                                leadingIcon = { Icon(Icons.Default.Delete, null) },
                                onClick = { menu = false; onDelete() },
                            )
                        }
                    }
                }
            }
        },
    )
}

@Composable
private fun Trouble(why: String, retry: () -> Unit) {
    Column(
        Modifier.fillMaxSize().padding(24.dp),
        verticalArrangement = Arrangement.spacedBy(16.dp, Alignment.CenterVertically),
        horizontalAlignment = Alignment.CenterHorizontally,
    ) {
        Text(why, style = MaterialTheme.typography.bodyLarge, color = MaterialTheme.colorScheme.error)
        Button(onClick = retry) { Text("Try again") }
    }
}

@Composable
private fun Nothing(dir: String) {
    Box(Modifier.fillMaxSize().padding(24.dp), Alignment.Center) {
        Text(
            if (dir.isEmpty()) "This folder is empty." else "Nothing in ${dir.trimEnd('/')}.",
            style = MaterialTheme.typography.bodyLarge,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
        )
    }
}

// A size a person reads rather than a number of bytes. Whole units below a megabyte,
// because a tenth of a kilobyte says nothing.
fun humanSize(bytes: Long): String = when {
    bytes < 1_024 -> "$bytes B"
    bytes < 1_048_576 -> "${bytes / 1_024} KB"
    bytes < 1_073_741_824 -> tenths(bytes, 1_048_576, "MB")
    else -> tenths(bytes, 1_073_741_824, "GB")
}

private fun tenths(bytes: Long, unit: Long, name: String): String {
    val scaled = bytes * 10 / unit
    return "${scaled / 10}.${scaled % 10} $name"
}
