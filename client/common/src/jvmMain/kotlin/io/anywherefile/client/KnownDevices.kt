package io.anywherefile.client

import java.io.File
import java.io.IOException

// The PCs this client has connected to before, one a line: the device id, then the gateway
// host:port it last answered at, the way ssh keeps its known hosts. A PC that is not in
// the file is shown as new, which is the moment to compare the fingerprint on the screen
// against the one `agent key` prints on the PC itself. After that the PC is quiet, until
// somebody forgets it and asks to be shown the fingerprint again.
//
// The id is all that is kept for trust. A device id is the digest of the device key, and
// verifyDeviceProof checks that on every connection, so a PC cannot turn up under an id
// whose key it does not hold. There is no second key for a file like this to catch.
//
// The address is kept for reachability (#254). When the browse cannot ask the network
// anything, multicast is blocked (the macOS local network permission refused, the access
// point isolating its clients), the address here is the only way in: the PCs remembered
// go on the list and are probed by reading the discovery document, which refuses to trust
// a PC until it holds the key the id is a digest of. Lines written before the address was
// kept, with only an id on them, are still read; they just give no address to fall back on.
class KnownDevices(private val file: File) {
    private val pcs = LinkedHashMap<String, String?>() // device id to address, null when none is stored
    private var loaded = false

    // knew reports whether this PC was already known, and records it either way. It is
    // called once per connection, because what it answers is what the screen then says.
    @Synchronized
    fun knew(id: String): Boolean {
        load()
        if (pcs.containsKey(id)) return true
        pcs[id] = null
        write()
        return false
    }

    // remember records where this PC answered last. It is called once the discovery
    // document has proved the PC holds its key, so an imposter cannot take over the
    // address of a PC this client has met.
    @Synchronized
    fun remember(id: String, address: String) {
        load()
        if (pcs[id] == address) return
        pcs[id] = address
        write()
    }

    @Synchronized
    fun forget(id: String) {
        load()
        if (pcs.remove(id) != null) write()
    }

    // remembered is every PC met before that has a place to try again at: what the browse
    // falls back on when the network cannot be asked.
    @Synchronized
    fun remembered(): Map<String, String> {
        load()
        return pcs.entries.mapNotNull { (id, address) -> address?.let { id to it } }.toMap()
    }

    private fun load() {
        if (loaded) return
        loaded = true
        try {
            file.forEachLine { line ->
                val parts = line.trim().split(' ', limit = 2)
                if (parts[0].isNotEmpty()) pcs[parts[0]] = parts.getOrNull(1)
            }
        } catch (e: IOException) {
            // Nothing written yet, or a file this app may not read. Every PC is new until
            // one can be.
        }
    }

    private fun write() {
        try {
            file.parentFile?.mkdirs()
            file.writeText(
                pcs.entries.joinToString("\n", postfix = "\n") { (id, address) ->
                    if (address == null) id else "$id $address"
                },
            )
        } catch (e: IOException) {
            // A PC that cannot be recorded is shown as new again next time, which is the
            // safe way round.
        }
    }
}
