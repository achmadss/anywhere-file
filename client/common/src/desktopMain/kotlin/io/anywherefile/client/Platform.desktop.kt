package io.anywherefile.client

import androidx.compose.material3.ColorScheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import java.awt.Desktop
import java.net.URI

@Composable
actual fun anywhereColors(dark: Boolean): ColorScheme = if (dark) darkColorScheme() else lightColorScheme()

// A window has a close button and the window manager owns it.
@Composable
actual fun SystemBack(onBack: () -> Unit) = Unit

// The desktop's own browser, through the JDK. A machine with no desktop session at all has
// nothing to open a page with, which the screen reports rather than swallowing.
@Composable
actual fun rememberBrowser(): (String) -> Unit = remember {
    { url -> Desktop.getDesktop().browse(URI(url)) }
}
