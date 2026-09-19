package io.anywherefile.client

import kotlin.test.Test
import kotlin.test.assertEquals

// What the file browser works out for itself. Everything else on that screen is an answer
// from the PC, and AgentOnTheLanTest carries a real file to one and back.
class FileBrowserTest {
    @Test
    fun goingUpEndsAtTheTop() {
        assertEquals("a/b/", parentOf("a/b/c/"))
        assertEquals("a/", parentOf("a/b/"))
        assertEquals("", parentOf("a/"))
        // The top is where the browser leaves the PC rather than going up again.
        assertEquals("", parentOf(""))
    }

    @Test
    fun aSizeReadsLikeASize() {
        assertEquals("0 B", humanSize(0))
        assertEquals("1023 B", humanSize(1023))
        assertEquals("1 KB", humanSize(1024))
        assertEquals("1.0 MB", humanSize(1_048_576))
        assertEquals("1.5 MB", humanSize(1_572_864))
        assertEquals("5.0 GB", humanSize(5_368_709_120))
        // A file bigger than the unit above it does not roll over early.
        assertEquals("1023 KB", humanSize(1_048_575))
    }
}
