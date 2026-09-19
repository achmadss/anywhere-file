package io.anywherefile.client

import android.app.DownloadManager
import android.net.Uri
import android.os.Environment
import android.webkit.URLUtil
import android.webkit.ValueCallback
import android.webkit.WebChromeClient
import android.webkit.WebView
import android.webkit.WebViewClient
import androidx.activity.compose.BackHandler
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.viewinterop.AndroidView
import androidx.core.net.toUri

// An application's own pages, shown inside this app (#99). The desktop hands the relay's
// address to the system browser and Android cannot: a backgrounded app is frozen within
// about a minute, and the relay stops answering halfway through whatever it was carrying.
// A WebView keeps the app in front of the person, so the relay keeps running.
//
// The WebView talks plain HTTP to 127.0.0.1 and never sees the PC's certificate, so there
// is nothing to check here. The relay does the checking, the same code as the desktop's.
@Composable
fun AppScreen(url: String, onBack: () -> Unit) {
    // What the page is waiting for when it asked for a file to upload. Held across the
    // trip to the picker, and answered either way: a page whose chooser never comes back
    // will not ask again.
    var waiting by remember { mutableStateOf<ValueCallback<Array<Uri>>?>(null) }
    val pick = rememberLauncherForActivityResult(ActivityResultContracts.GetMultipleContents()) { chosen ->
        waiting?.onReceiveValue(chosen.takeIf { it.isNotEmpty() }?.toTypedArray())
        waiting = null
    }
    var web by remember { mutableStateOf<WebView?>(null) }
    BackHandler {
        val w = web
        if (w != null && w.canGoBack()) w.goBack() else onBack()
    }
    AndroidView(
        modifier = Modifier.fillMaxSize(),
        factory = { context ->
            WebView(context).apply {
                web = this
                settings.javaScriptEnabled = true
                settings.domStorageEnabled = true
                // Nothing on these pages has any business reading the phone's own files.
                settings.allowFileAccess = false
                settings.allowContentAccess = false
                // Everything stays in the WebView. A link out would land in the browser,
                // which cannot reach the relay once this app is behind it.
                webViewClient = WebViewClient()
                webChromeClient = object : WebChromeClient() {
                    override fun onShowFileChooser(
                        view: WebView,
                        callback: ValueCallback<Array<Uri>>,
                        params: FileChooserParams,
                    ): Boolean {
                        waiting?.onReceiveValue(null)
                        waiting = callback
                        pick.launch("*/*")
                        return true
                    }
                }
                // A WebView does not download; it hands the URL back and leaves it to the
                // app. The download manager is the one that survives the person leaving.
                setDownloadListener { link, agent, disposition, mime, _ ->
                    val name = URLUtil.guessFileName(link, disposition, mime)
                    val request = DownloadManager.Request(link.toUri())
                        .setMimeType(mime)
                        .addRequestHeader("User-Agent", agent)
                        .setDestinationInExternalPublicDir(Environment.DIRECTORY_DOWNLOADS, name)
                        .setNotificationVisibility(DownloadManager.Request.VISIBILITY_VISIBLE_NOTIFY_COMPLETED)
                    context.getSystemService(DownloadManager::class.java).enqueue(request)
                }
                loadUrl(url)
            }
        },
    )
}
