package io.anywherefile.client

import androidx.compose.foundation.layout.padding
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp

// The whole client, shown by both apps. The device list comes with #98.
@Composable
fun App() {
    MaterialTheme {
        Text("anywhere-file", modifier = Modifier.padding(24.dp))
    }
}
