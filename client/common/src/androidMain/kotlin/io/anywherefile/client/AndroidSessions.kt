package io.anywherefile.client

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import java.security.GeneralSecurityException
import java.security.KeyStore
import java.util.Base64
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

// The session token, kept the way Android keeps a secret (#100). An AES key is generated in
// the Keystore, where it cannot be read back out, and the token is stored encrypted under it
// in this app's own preferences. The preferences file is private to the app, and the key is
// what makes the token inside it belong to this app on this phone, so a copy of the file, a
// backup, or a rooted reader without the Keystore gets ciphertext.
//
// This is what the Keystore is for. androidx.security's EncryptedSharedPreferences is
// deprecated and its advice points here: make a key, keep the ciphertext yourself.
class AndroidSessions(context: Context) : SessionStore {
    private val prefs = context.getSharedPreferences("session", Context.MODE_PRIVATE)

    override fun token(): String? {
        val stored = prefs.getString(TOKEN, null) ?: return null
        return try {
            val raw = Base64.getDecoder().decode(stored)
            if (raw.size <= IV_BYTES) return null
            val cipher = Cipher.getInstance(TRANSFORM)
            cipher.init(Cipher.DECRYPT_MODE, key(), GCMParameterSpec(TAG_BITS, raw, 0, IV_BYTES))
            cipher.doFinal(raw, IV_BYTES, raw.size - IV_BYTES).decodeToString()
        } catch (e: GeneralSecurityException) {
            // The key is gone: the app's data was restored onto another phone, or the
            // Keystore entry was cleared. There is no session to be read, so there is none.
            forget()
            null
        } catch (e: IllegalArgumentException) {
            forget()
            null
        }
    }

    override fun save(token: String) {
        val cipher = Cipher.getInstance(TRANSFORM)
        cipher.init(Cipher.ENCRYPT_MODE, key())
        val body = cipher.doFinal(token.encodeToByteArray())
        val blob = Base64.getEncoder().encodeToString(cipher.iv + body)
        prefs.edit().putString(TOKEN, blob).apply()
    }

    override fun forget() {
        prefs.edit().remove(TOKEN).apply()
    }

    // key returns the Keystore key, making it on first use. It needs no user authentication:
    // this client answers a notification and reaches a PC while the phone is in a pocket, and
    // the token is not the last thing standing between a thief and the files on a PC.
    private fun key(): SecretKey {
        val store = KeyStore.getInstance(ANDROID_KEYSTORE).apply { load(null) }
        (store.getKey(ALIAS, null) as? SecretKey)?.let { return it }
        val generator = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, ANDROID_KEYSTORE)
        generator.init(
            KeyGenParameterSpec.Builder(ALIAS, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setRandomizedEncryptionRequired(true)
                .build(),
        )
        return generator.generateKey()
    }

    private companion object {
        const val ANDROID_KEYSTORE = "AndroidKeyStore"
        const val ALIAS = "anywhere-file session"
        const val TOKEN = "token"
        const val TRANSFORM = "AES/GCM/NoPadding"
        const val IV_BYTES = 12
        const val TAG_BITS = 128
    }
}
