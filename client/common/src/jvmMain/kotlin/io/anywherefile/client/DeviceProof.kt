package io.anywherefile.client

import org.bouncycastle.math.ec.rfc8032.Ed25519
import java.security.MessageDigest
import java.security.cert.X509Certificate

// The client half of #96. Nothing signs the gateway's certificate except the PC itself, so
// there is no authority to ask whether it is genuine. What the client trusts instead is the
// PC's device key: the discovery document carries that key and the key's signature over the
// certificate's public key. Both checks together say the PC holding the device key is the PC
// on the other end of this connection.
//
// This is device/agent/tls.go's verifyDeviceProof in Kotlin, and DeviceProofTest checks the
// two against a document and certificate the agent itself produced.
//
// The signature is checked by Bouncy Castle rather than by the JVM's own Ed25519, because
// Android has had one only since API 33 and this app runs on phones four years older.

// The device key signs this in front of the certificate's key, so the signature cannot be
// lifted from somewhere else the key is used.
private const val PROOF_CONTEXT = "anywhere-file lan-tls v1\n"

// WrongDevice is a PC that failed to prove it is the one being looked for. The message is
// shown to a person, so it says which PC and what did not add up.
class WrongDevice(message: String) : Exception(message)

// verifyDeviceProof decides whether the PC serving this certificate is the device being
// looked for. It throws rather than returning false, because every caller either goes on to
// use the connection or shows the reason it stopped.
fun verifyDeviceProof(deviceId: String, publicKey: String, proof: String, certificate: X509Certificate) {
    val key = hexBytes(publicKey)
    if (key == null || key.size != Ed25519.PUBLIC_KEY_SIZE) throw WrongDevice("This PC offered no usable public key.")
    // The device id is the digest of the key, so a PC cannot hold a key under an id that is
    // not its own.
    if (!sha256(key).contentEquals(hexBytes(deviceId))) {
        throw WrongDevice("This PC's key is not the one ${deviceId.take(8)} stands for.")
    }
    val signature = hexBytes(proof)
    if (signature == null || signature.size != Ed25519.SIGNATURE_SIZE) {
        throw WrongDevice("This PC's proof is not a signature.")
    }
    // The message is the context and the digest of the certificate's key, exactly as the
    // agent assembles it.
    val signed = PROOF_CONTEXT.toByteArray() + sha256(certificate.publicKey.encoded)
    if (!Ed25519.verify(signature, 0, key, 0, signed, 0, signed.size)) {
        throw WrongDevice("This PC did not sign its certificate with ${deviceId.take(8)}'s key, so it is a different PC.")
    }
}

private fun sha256(b: ByteArray): ByteArray = MessageDigest.getInstance("SHA-256").digest(b)
