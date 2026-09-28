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

// A way to hand a URL to this platform's own browser. The account pages are the browser's
// job: creating an account and resetting a password happen there, so the client draws no
// form for either and never holds a token that arrives by mail (#100).
@Composable
expect fun rememberBrowser(): (String) -> Unit

// Hands text to whatever this platform shares with: the share sheet on Android. The desktop
// has no share sheet, so the text goes on the clipboard and the answer says so for the screen.
@Composable
expect fun rememberShare(): (String) -> String?

@Composable
fun AnywhereFile(content: @Composable () -> Unit) {
    MaterialTheme(colorScheme = anywhereColors(isSystemInDarkTheme()), content = content)
}
