package io.anywherefile.client

import java.io.File
import java.io.IOException

// The PCs this client has connected to before, one device id a line, the way ssh keeps its
// known hosts. A PC that is not in the file is shown as new, which is the moment to compare
// the fingerprint on the screen against the one `agent key` prints on the PC itself. After
// that the PC is quiet, until somebody forgets it and asks to be shown the fingerprint
// again.
//
// The id is all that is kept. A device id is the digest of the device key, and
// verifyDeviceProof checks that on every connection, so a PC cannot turn up under an id
// whose key it does not hold. There is no second key for a file like this to catch.
class KnownDevices(private val file: File) {
    private val ids = HashSet<String>()
    private var loaded = false

    // knew reports whether this PC was already known, and records it either way. It is
    // called once per connection, because what it answers is what the screen then says.
    @Synchronized
    fun knew(id: String): Boolean {
        load()
        if (!ids.add(id)) return true
        write()
        return false
    }

    @Synchronized
    fun forget(id: String) {
        load()
        if (ids.remove(id)) write()
    }

    private fun load() {
        if (loaded) return
        loaded = true
        try {
            file.forEachLine { line -> line.trim().takeIf { it.isNotEmpty() }?.let(ids::add) }
        } catch (e: IOException) {
            // Nothing written yet, or a file this app may not read. Every PC is new until
            // one can be.
        }
    }

    private fun write() {
        try {
            file.parentFile?.mkdirs()
            file.writeText(ids.joinToString("\n", postfix = "\n"))
        } catch (e: IOException) {
            // A PC that cannot be recorded is shown as new again next time, which is the
            // safe way round.
        }
    }
}
