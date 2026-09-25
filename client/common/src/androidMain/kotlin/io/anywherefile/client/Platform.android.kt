package io.anywherefile.client

import android.content.Intent
import android.net.Uri
import android.os.Build
import androidx.activity.compose.BackHandler
import androidx.compose.material3.ColorScheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.material3.dynamicDarkColorScheme
import androidx.compose.material3.dynamicLightColorScheme
import androidx.compose.material3.lightColorScheme
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
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

// The phone's own browser, through an intent. The activity is the context here, so the
// intent starts in this task, and the person comes back with the back gesture.
@Composable
actual fun rememberBrowser(): (String) -> Unit {
    val context = LocalContext.current
    return remember(context) {
        { url -> context.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(url))) }
    }
}

@Composable
actual fun rememberShare(): (String) -> String? {
    val context = LocalContext.current
    return remember(context) {
        { text ->
            val send = Intent(Intent.ACTION_SEND).setType("text/plain").putExtra(Intent.EXTRA_TEXT, text)
            context.startActivity(Intent.createChooser(send, null))
            null
        }
    }
}
