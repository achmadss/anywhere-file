package io.anywherefile.client

import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.material3.ColorScheme
import androidx.compose.material3.MaterialTheme
import androidx.compose.runtime.Composable

// The two things a screen here cannot do for itself. Android draws its colours from the
// wallpaper and has a back button; the desktop has neither.

@Composable
expect fun anywhereColors(dark: Boolean): ColorScheme

// Runs on the system back gesture, where there is one. The desktop has nothing to bind.
@Composable
expect fun SystemBack(onBack: () -> Unit)

@Composable
fun AnywhereFile(content: @Composable () -> Unit) {
    MaterialTheme(colorScheme = anywhereColors(isSystemInDarkTheme()), content = content)
}
