package io.anywherefile.client

import kotlin.io.encoding.Base64
import kotlin.io.encoding.ExperimentalEncodingApi

// fingerprintOf is a device id in the shape `agent key` prints it, for a person comparing a
// PC on the screen against the PC in front of them. The id and the fingerprint are the same
// digest of the same key, one written in hexadecimal and one the way OpenSSH writes a host
// key, so the id is all this needs.
@OptIn(ExperimentalEncodingApi::class)
fun fingerprintOf(deviceId: String): String {
    val digest = hexBytes(deviceId) ?: return deviceId
    return "SHA256:" + Base64.Default.withPadding(Base64.PaddingOption.ABSENT).encode(digest)
}

// hexBytes returns null for anything that is not an even run of hexadecimal. Everything it
// is given came off the network.
internal fun hexBytes(s: String): ByteArray? {
    if (s.length % 2 != 0) return null
    return ByteArray(s.length / 2) {
        val high = digit(s[it * 2])
        val low = digit(s[it * 2 + 1])
        if (high < 0 || low < 0) return null
        ((high shl 4) or low).toByte()
    }
}

private fun digit(c: Char) = when (c) {
    in '0'..'9' -> c - '0'
    in 'a'..'f' -> c - 'a' + 10
    in 'A'..'F' -> c - 'A' + 10
    else -> -1
}
