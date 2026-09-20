package io.anywherefile.client

import androidx.compose.material3.ColorScheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable

@Composable
actual fun anywhereColors(dark: Boolean): ColorScheme = if (dark) darkColorScheme() else lightColorScheme()

// A window has a close button and the window manager owns it.
@Composable
actual fun SystemBack(onBack: () -> Unit) = Unit
