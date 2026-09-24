package io.anywherefile.client

import java.io.File
import java.nio.file.Files
import java.nio.file.attribute.PosixFilePermissions
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertNull
import kotlin.test.assertTrue

// Where the session token is kept (#100). The store this machine would really use is asked
// for a round trip, and the file it falls back to when there is no keychain is held to its
// permissions, because a file anybody can read is not a place for a session.
class SessionsTest {
    @Test
    fun theStoreOnThisMachineKeepsASession() {
        // A name of its own, so this never writes over a session somebody is signed in with
        // (the agent's keyring service is a variable for the same reason).
        sessionService = "dev.anywherefile.client.test"
        val file = File.createTempFile("session", "").apply { delete() }
        val store = DesktopSessions(file)
        store.forget()
        assertNull(store.token(), "a store that was never written to held something")

        store.save("a-session-token")
        assertEquals("a-session-token", store.token(), "what came back out")

        // A second store over the same place is what the next run of the app sees.
        assertEquals("a-session-token", DesktopSessions(file).token(), "the token did not survive a restart")

        store.forget()
        assertNull(store.token(), "the token is still there after forgetting it")
    }

    @Test
    fun theFileFallbackIsOnlyItsOwners() {
        val home = Files.createTempDirectory("anywhere-file-test").toFile()
        // A directory of its own for the store to make, because that is the one it is allowed
        // to tighten: an existing directory belongs to whoever made it.
        val dir = File(home, "anywhere-file")
        val file = File(dir, "session")
        val store = SeedFile(file)
        store.write("a-session-token")

        assertEquals("a-session-token", store.read(), "what the file holds")
        assertEquals(
            "rw-------",
            PosixFilePermissions.toString(Files.getPosixFilePermissions(file.toPath())),
            "the mode a token file is written with",
        )
        assertEquals(
            "rwx------",
            PosixFilePermissions.toString(Files.getPosixFilePermissions(dir.toPath())),
            "the mode its directory is tightened to",
        )
        assertTrue(store.caveat.isNotBlank(), "a file store says nothing about being one")

        store.forget()
        assertNull(store.read(), "the token is still there after forgetting it")
    }

    @Test
    fun aTokenFileAnybodyCanReadIsNotUsed() {
        val file = File.createTempFile("session", "")
        file.writeText("a-session-token")
        Files.setPosixFilePermissions(file.toPath(), PosixFilePermissions.fromString("rw-r--r--"))

        // Reading it anyway would mean a session someone else may also have read.
        assertNull(SeedFile(file).read(), "a world-readable token file was used")
    }
}
