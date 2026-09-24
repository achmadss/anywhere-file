package io.anywherefile.client

import java.io.File
import java.io.IOException
import java.nio.file.Files
import java.nio.file.Path
import java.nio.file.StandardOpenOption
import java.nio.file.attribute.PosixFilePermissions

// The session token, kept where this system keeps secrets (#100). There is no keystore call
// in the JDK, so each system is asked through the tool it ships: the login keychain on macOS,
// libsecret on Linux, and the Data Protection API on Windows. A machine with none of them
// still works, and the file it falls back to says so on the screen, which is the same
// bargain the agent makes for a device key (device/agent/seedstore.go).
//
// The service name is a variable, the way the agent's is, so a test run uses a name of its
// own and never touches a session somebody is signed in with.

var sessionService = "dev.anywherefile.client"
private const val ACCOUNT = "session"

class DesktopSessions(private val file: File) : SessionStore {
    private val secret: Secret = secretStore(file)

    override fun token(): String? = secret.read()

    override fun save(token: String) {
        secret.write(token)
    }

    override fun forget() {
        secret.forget()
    }

    override val caveat: String? get() = secret.caveat
}

// What one system's secret store needs to do.
internal interface Secret {
    fun read(): String?
    fun write(token: String)
    fun forget()

    // A sentence for the screen when this is not a secret store.
    val caveat: String? get() = null
}

// secretStore picks what this system has, best first.
internal fun secretStore(file: File): Secret {
    val os = System.getProperty("os.name").orEmpty().lowercase()
    return when {
        os.startsWith("mac") -> Keychain()
        os.startsWith("windows") -> Dpapi(File(file.parentFile, "session.dpapi"))
        onPath("secret-tool") -> Libsecret()
        else -> SeedFile(file)
    }
}

// The login keychain, through the tool every Mac ships. The entry is a generic password under
// this service name, which is what Keychain Access shows and what the agent's own keyring
// store writes for a device key.
internal class Keychain : Secret {
    override fun read(): String? =
        run(SECURITY, listOf("find-generic-password", "-s", sessionService, "-w"))?.trim()?.ifEmpty { null }

    override fun write(token: String) {
        run(SECURITY, listOf("add-generic-password", "-U", "-s", sessionService, "-a", ACCOUNT, "-w", token))
    }

    override fun forget() {
        run(SECURITY, listOf("delete-generic-password", "-s", sessionService))
    }
}

// libsecret, which is what GNOME Keyring and KWallet answer for. The token goes in on stdin,
// so it is never an argument another process could read out of the process list.
internal class Libsecret : Secret {
    override fun read(): String? =
        run("secret-tool", listOf("lookup", "service", sessionService, "account", ACCOUNT))?.trim()?.ifEmpty { null }

    override fun write(token: String) {
        run(
            "secret-tool",
            listOf("store", "--label=anywhere-file session", "service", sessionService, "account", ACCOUNT),
            stdin = token,
        )
    }

    override fun forget() {
        run("secret-tool", listOf("clear", "service", sessionService, "account", ACCOUNT))
    }
}

// The Windows Data Protection API, through PowerShell. The ciphertext can only be read back
// by this Windows account on this machine, which is what DPAPI is for: it is tied to the
// login rather than to a key this process holds. The token travels in the environment of the
// child process rather than in an argument, and the JDK has no call for this, so one
// PowerShell line beats a native binding.
internal class Dpapi(private val file: File) : Secret {
    override fun read(): String? =
        run(POWERSHELL, POWERSHELL_FLAGS + UNPROTECT, env = mapOf("RFM_FILE" to file.path))?.trim()?.ifEmpty { null }

    override fun write(token: String) {
        file.parentFile?.mkdirs()
        run(
            POWERSHELL,
            POWERSHELL_FLAGS + PROTECT,
            env = mapOf("RFM_FILE" to file.path, "RFM_SECRET" to token),
        )
    }

    override fun forget() {
        file.delete()
    }
}

// Where a system has no keychain: a 0600 file in a 0700 directory, and a sentence saying so.
// A file written wider than that is one somebody else may read, so it is not read back.
internal class SeedFile(private val file: File) : Secret {
    override val caveat =
        "This system has no keychain, so the session is kept in a file only your account can read."

    override fun read(): String? {
        if (!file.isFile) return null
        if (readableByOthers(file)) return null
        return try {
            file.readText().trim().ifEmpty { null }
        } catch (e: IOException) {
            null
        }
    }

    override fun write(token: String) {
        try {
            val dir = file.parentFile
            // The directory is tightened only when this made it. An existing one belongs to
            // whoever made it, and the agent's own store leaves it alone for the same reason.
            if (dir != null && !dir.exists() && dir.mkdirs()) tighten(dir.toPath())
            val path = file.toPath()
            if (Files.notExists(path)) Files.createFile(path, PosixFilePermissions.asFileAttribute(FILE_PERMISSIONS))
            Files.writeString(path, "$token\n", StandardOpenOption.TRUNCATE_EXISTING, StandardOpenOption.WRITE)
        } catch (e: IOException) {
            // Nothing to do about it here: the next start finds no session, which is the
            // safe way round.
        } catch (e: UnsupportedOperationException) {
            file.writeText("$token\n")
        }
    }

    override fun forget() {
        file.delete()
    }
}

// Anything the owner's group or anybody else may touch is not this file any more.
private fun readableByOthers(file: File): Boolean = try {
    PosixFilePermissions.toString(Files.getPosixFilePermissions(file.toPath())).substring(3).any { it != '-' }
} catch (e: IOException) {
    false
} catch (e: UnsupportedOperationException) {
    false
}

private fun tighten(path: Path) {
    try {
        Files.setPosixFilePermissions(path, PosixFilePermissions.fromString("rwx------"))
    } catch (e: UnsupportedOperationException) {
        // A filesystem with no permissions to set. Windows is not here: it has DPAPI.
    }
}

// run starts a tool and hands back its output, or null when it failed or is not installed.
internal fun run(
    tool: String,
    args: List<String>,
    stdin: String? = null,
    env: Map<String, String> = emptyMap(),
): String? = try {
    val process = ProcessBuilder(listOf(tool) + args)
        .redirectError(ProcessBuilder.Redirect.DISCARD)
        .apply { environment().putAll(env) }
        .start()
    process.outputStream.use { if (stdin != null) it.write(stdin.encodeToByteArray()) }
    val out = process.inputStream.readBytes().decodeToString()
    if (process.waitFor() != 0) null else out
} catch (e: IOException) {
    null
}

private fun onPath(tool: String) = System.getenv("PATH").orEmpty()
    .split(File.pathSeparator)
    .any { File(it, tool).canExecute() }

private val FILE_PERMISSIONS = PosixFilePermissions.fromString("rw-------")

private const val SECURITY = "/usr/bin/security"
private const val POWERSHELL = "powershell"
private val POWERSHELL_FLAGS = listOf("-NoProfile", "-NonInteractive", "-Command")
private const val PROTECT =
    "\$in=[Text.Encoding]::UTF8.GetBytes(\$env:RFM_SECRET);" +
        "[IO.File]::WriteAllBytes(\$env:RFM_FILE,[Security.Cryptography.ProtectedData]::Protect(\$in,\$null," +
        "[Security.Cryptography.DataProtectionScope]::CurrentUser))"
private const val UNPROTECT =
    "\$out=[Security.Cryptography.ProtectedData]::Unprotect([IO.File]::ReadAllBytes(\$env:RFM_FILE),\$null," +
        "[Security.Cryptography.DataProtectionScope]::CurrentUser);[Text.Encoding]::UTF8.GetString(\$out)"
