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
        val transfers = AndroidTransfers(applicationContext)
        // The account and where its token is kept (#100). Android keeps it encrypted under a
        // Keystore key; the server address is a plain file beside it, because an address is
        // not a secret.
        val session = Session(AndroidSessions(applicationContext), ServerAddress(File(filesDir, "server")))
        setContent {
            val dark = isSystemInDarkTheme()
            // There is no bar behind the status icons, so they are drawn over whatever the
            // screen is and have to be the opposite of it to stay readable.
            SideEffect {
                WindowCompat.getInsetsController(window, window.decorView).isAppearanceLightStatusBars = !dark
            }
            var allowed by remember { mutableStateOf(localNetworkAllowed()) }
            var denied by remember { mutableStateOf(false) }
            // The folder the person is looking at, or null for the home screen.
            var browsing by remember { mutableStateOf<Files?>(null) }
            // True while the sign-in screen is up (#100). Signing in does not need the local
            // network, so this is reachable from the screen that asks for it as well.
            var signingIn by remember { mutableStateOf(false) }
            val ask = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
                allowed = granted
                denied = !granted
            }
            val pick = rememberLauncherForActivityResult(ActivityResultContracts.GetContent()) { transfers.chose(it) }
            SideEffect { transfers.ask = { pick.launch("*/*") } }
            val onOpen = { device: Device, app: String ->
                openFiles(device, app, devices, transfers) { browsing = it }
            }
            val onSignIn = { signingIn = true }
            AnywhereFile {
                val files = browsing
                when {
                    files != null -> FileBrowser(files) { browsing = null }
                    signingIn -> SignIn(session) { signingIn = false }
                    allowed -> {
                        DisposableEffect(Unit) {
                            discovery.start()
                            onDispose { discovery.stop() }
                        }
                        Home(devices, session, onSignIn, onOpen)
                    }
                    else -> LocalNetworkGate(
                        denied,
                        onAsk = { ask.launch(ACCESS_LOCAL_NETWORK) },
                        onPick = discovery::pick,
                        devices,
                        session,
                        onSignIn,
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
// answer was no: Android shows the devices it can see and hands over the one chosen. The
// account is here as well, because reaching a PC from outside the house is the one thing
// that does not need this permission.
@Composable
private fun LocalNetworkGate(
    denied: Boolean,
    onAsk: () -> Unit,
    onPick: () -> Unit,
    devices: Devices,
    session: Account,
    onSignIn: () -> Unit,
    onOpen: (Device, String) -> Unit,
) {
    Column(
        Modifier.safeDrawingPadding().fillMaxSize().padding(24.dp),
        verticalArrangement = Arrangement.spacedBy(16.dp),
    ) {
        Text("Devices on this network", style = MaterialTheme.typography.headlineSmall)
        Text(
            "anywhere-file finds your devices by asking this network which of them are running it too. Android checks with you first.",
            style = MaterialTheme.typography.bodyMedium,
        )
        Button(onClick = onAsk) { Text("Allow") }
        if (denied) {
            Text(
                "Without it, you can still pick one device at a time from Android's own list.",
                style = MaterialTheme.typography.bodyMedium,
            )
            Button(onClick = onPick) { Text("Choose a device") }
        }
        if (devices.found.isNotEmpty()) DeviceRows(devices, onOpen)
        AccountCard(session, onSignIn)
    }
}
