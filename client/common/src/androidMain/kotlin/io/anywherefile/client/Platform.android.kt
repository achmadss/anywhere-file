package io.anywherefile.client

import android.os.Build
import androidx.activity.compose.BackHandler
import androidx.compose.material3.ColorScheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.dynamicDarkColorScheme
import androidx.compose.material3.dynamicLightColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.ui.platform.LocalContext

// From Android 12 the system holds a palette taken from the wallpaper, and using it is
// what makes an app look like it belongs on the phone it is installed on. Below that there
// is nothing to take and the Material baseline stands.
@Composable
actual fun anywhereColors(dark: Boolean): ColorScheme {
    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
        val context = LocalContext.current
        return if (dark) dynamicDarkColorScheme(context) else dynamicLightColorScheme(context)
    }
    return if (dark) darkColorScheme() else lightColorScheme()
}

@Composable
actual fun SystemBack(onBack: () -> Unit) = BackHandler(onBack = onBack)
