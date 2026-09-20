package io.anywherefile.client

import android.content.ContentValues
import android.content.Context
import android.net.Uri
import android.os.Build
import android.os.Environment
import android.provider.MediaStore
import android.provider.OpenableColumns
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import java.io.File
import java.io.InputStream

// Where a file comes from and where one lands, on a phone (#155).
class AndroidTransfers(private val context: Context) : Transfers {
    // Set by the screen, because launching the system file picker takes a composable and
    // the answer arrives as an activity result.
    var ask: (() -> Unit)? = null
    private var waiting: CompletableDeferred<Uri?>? = null

    fun chose(uri: Uri?) {
        waiting?.complete(uri)
        waiting = null
    }

    override suspend fun pick(): Outgoing? {
        val answer = CompletableDeferred<Uri?>()
        waiting = answer
        withContext(Dispatchers.Main) { ask?.invoke() }
        val uri = answer.await() ?: return null
        var name = uri.lastPathSegment ?: "file"
        var length = -1L
        // A content URI is not a path. The name and the size are columns the app that
        // handed the file over fills in.
        context.contentResolver.query(uri, null, null, null, null)?.use { row ->
            val nameAt = row.getColumnIndex(OpenableColumns.DISPLAY_NAME)
            val sizeAt = row.getColumnIndex(OpenableColumns.SIZE)
            if (row.moveToFirst()) {
                if (nameAt >= 0 && !row.isNull(nameAt)) name = row.getString(nameAt)
                if (sizeAt >= 0 && !row.isNull(sizeAt)) length = row.getLong(sizeAt)
            }
        }
        return Outgoing(name, length) {
            context.contentResolver.openInputStream(uri) ?: error("that file is not there any more")
        }
    }

    override fun save(name: String, body: InputStream): String {
        // From Android 10 a file goes into the phone's own Downloads collection, which
        // every file manager shows and which needs no permission. Before that, writing
        // there needed one, so it goes in this app's folder instead.
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            val resolver = context.contentResolver
            val landing = ContentValues().apply {
                put(MediaStore.Downloads.DISPLAY_NAME, name)
                put(MediaStore.Downloads.IS_PENDING, 1)
            }
            val uri = resolver.insert(MediaStore.Downloads.EXTERNAL_CONTENT_URI, landing)
                ?: error("this phone would not take a file called $name")
            val out = resolver.openOutputStream(uri) ?: error("this phone would not open $name to write")
            out.use { body.copyTo(it) }
            resolver.update(uri, ContentValues().apply { put(MediaStore.Downloads.IS_PENDING, 0) }, null, null)
            return "Saved to Downloads"
        }
        val dir = context.getExternalFilesDir(Environment.DIRECTORY_DOWNLOADS) ?: context.filesDir
        val file = File(dir, name)
        file.outputStream().use { body.copyTo(it) }
        return "Saved to ${file.path}"
    }
}
