package io.anywherefile.client

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.safeDrawingPadding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.automirrored.filled.ArrowBack
import androidx.compose.material.icons.outlined.Cloud
import androidx.compose.material.icons.outlined.CloudOff
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.ExperimentalMaterial3Api
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
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext

// The sign-in screen (#100), and the account card the home screen shows in its place. Signing
// in is what makes a PC reachable from outside the house; on the same network there is
// nothing here a person needs.

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun SignIn(session: Account, onLeave: () -> Unit) {
    var server by remember { mutableStateOf(session.server) }
    var email by remember { mutableStateOf("") }
    var password by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    var trouble by remember { mutableStateOf<String?>(null) }
    val browser = rememberBrowser()
    val scope = rememberCoroutineScope()
    SystemBack(onLeave)

    fun carry(what: suspend () -> Unit) {
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

    // A page on the website, opened where a page belongs. The address is cleaned first, so a
    // typo in the address says so instead of opening a page that answers nothing.
    fun open(path: String) {
        trouble = null
        try {
            browser(cleanAddress(server) + path)
        } catch (e: Exception) {
            trouble = e.message ?: "This system has no browser to open that page with."
        }
    }

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text("Sign in") },
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
            Text(
                "An account reaches your PCs from anywhere, through the server. On this network it is not needed.",
                style = MaterialTheme.typography.bodyMedium,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            OutlinedTextField(
                value = server,
                onValueChange = { server = it },
                label = { Text("Server") },
                singleLine = true,
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri),
                modifier = Modifier.fillMaxWidth(),
            )
            OutlinedTextField(
                value = email,
                onValueChange = { email = it },
                label = { Text("Email") },
                singleLine = true,
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Email),
                modifier = Modifier.fillMaxWidth(),
            )
            OutlinedTextField(
                value = password,
                onValueChange = { password = it },
                label = { Text("Password") },
                singleLine = true,
                visualTransformation = PasswordVisualTransformation(),
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Password),
                modifier = Modifier.fillMaxWidth(),
            )
            if (busy) LinearProgressIndicator(Modifier.fillMaxWidth())
            trouble?.let {
                Text(it, style = MaterialTheme.typography.bodyMedium, color = MaterialTheme.colorScheme.error)
            }
            Button(
                onClick = { carry { session.signIn(server, email, password); onLeave() } },
                enabled = !busy && email.isNotBlank() && password.isNotEmpty(),
                modifier = Modifier.fillMaxWidth(),
            ) {
                Text("Sign in")
            }
            TextButton(onClick = { open("/signup") }, modifier = Modifier.fillMaxWidth()) {
                Text("Create an account")
            }
            TextButton(onClick = { open("/reset") }, modifier = Modifier.fillMaxWidth()) {
                Text("Forgot your password")
            }
        }
    }
}

// The account as the home screen shows it: who is signed in, a way in, and a way out. The
// sessions this client opens are for reaching PCs from outside the house, which is why this
// sits in the "Away from home" part of the screen (#162).
@Composable
fun AccountCard(session: Account, onSignIn: () -> Unit) {
    val scope = rememberCoroutineScope()
    // Once, on the first screen that shows the account: a session this device already holds
    // is checked against the server, so one that was signed out somewhere else is noticed
    // here rather than at the next sign in (#100). This lives in the card because the card is
    // what shows the answer, and it is on the screen that asks for the local network
    // permission as well as on the home screen.
    LaunchedEffect(Unit) { withContext(Dispatchers.Default) { session.resume() } }
    Card(
        Modifier.fillMaxWidth(),
        colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surfaceContainerLow),
    ) {
        Column(Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
            Row(
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(16.dp),
            ) {
                Icon(
                    if (session.signedIn) Icons.Outlined.Cloud else Icons.Outlined.CloudOff,
                    null,
                    Modifier.size(32.dp),
                    tint = MaterialTheme.colorScheme.onSurfaceVariant,
                )
                Column {
                    Text(
                        when {
                            !session.ready -> "Looking for an account on this device…"
                            !session.signedIn -> "Reaching your devices from anywhere needs an account."
                            session.email != null -> "Signed in as ${session.email}."
                            else -> "Signed in. The server has not answered yet."
                        },
                        style = MaterialTheme.typography.bodyMedium,
                        color = MaterialTheme.colorScheme.onSurfaceVariant,
                    )
                    session.trouble?.let {
                        Text(it, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.error)
                    }
                    // Where the token is kept, when it is not kept in a secret store. The
                    // person is entitled to know that much about their own session.
                    session.caveat?.let {
                        Text(
                            it,
                            style = MaterialTheme.typography.bodySmall,
                            color = MaterialTheme.colorScheme.onSurfaceVariant,
                        )
                    }
                }
            }
            if (session.ready) {
                if (session.signedIn) {
                    TextButton(onClick = { scope.launch { withContext(Dispatchers.Default) { session.signOut() } } }) {
                        Text("Sign out")
                    }
                } else {
                    Button(onClick = onSignIn) { Text("Sign in") }
                }
            }
        }
    }
}
