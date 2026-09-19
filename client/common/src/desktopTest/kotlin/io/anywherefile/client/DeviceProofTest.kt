package io.anywherefile.client

import java.io.ByteArrayInputStream
import java.security.cert.CertificateFactory
import java.security.cert.X509Certificate
import kotlin.test.Test
import kotlin.test.assertEquals
import kotlin.test.assertFailsWith

// The agent produced all of this, from a device key whose seed is the bytes 0 to 31:
// device/agent/tls.go made the certificate and signed the proof, and identity.go printed the
// id and the fingerprint. If the Kotlin below stops agreeing with it, the client and the
// agent have stopped agreeing about what proves a PC is itself.
class DeviceProofTest {
    private companion object {
        const val DEVICE_ID = "56475aa75463474c0285df5dbf2bcab73da651358839e9b77481b2eab107708c"
        const val PUBLIC_KEY = "03a107bff3ce10be1d70dd18e74bc09967e4d6309ba50d5f1ddc8664125531b8"
        const val FINGERPRINT = "SHA256:Vkdap1RjR0wChd9dvyvKtz2mUTWIOem3dIGy6rEHcIw"
        const val PROOF = "3662f71047d188fabf022018c890317c3604821af5c4d366611ed005ad163b9f8fed08263686d636d0406a12a0feb203a09d9a015a565535857996ba3982300d"
        const val CERT = "30820234308201d9a003020102020101300a06082a8648ce3d0403023059315730550603550403134e353634373561613735343633343734633032383564663564626632626361623733646136353133353838333965396237373438316232656162313037373038632e616e7977686572652d66696c65301e170d3730303130313030303030305a170d3731303230323030303030305a3059315730550603550403134e353634373561613735343633343734633032383564663564626632626361623733646136353133353838333965396237373438316232656162313037373038632e616e7977686572652d66696c653059301306072a8648ce3d020106082a8648ce3d0301070342000441586e6c1962190c8ab4e71626c5b5e2a6fe8ffa1743456b055d2901b599da7994895d7d296cee7bfcdb682300d6d2d59fbe8ebd61df405da2deb9993bdbd23ba3819130818e300e0603551d0f0101ff04040302078030130603551d25040c300a06082b06010505070301300c0603551d130101ff0402300030590603551d1104523050824e353634373561613735343633343734633032383564663564626632626361623733646136353133353838333965396237373438316232656162313037373038632e616e7977686572652d66696c65300a06082a8648ce3d040302034900304602210098bf583262dc4f1e8baeadf27368a17c2c0239d2e00782d56bdfca36aa6927af0221009f7f34c2c4a9963e643266cc3f0f214eee540066e9c032cb0093258f4475fa0b"
        // A second PC's certificate, from a different device key.
        const val OTHER_CERT = "30820233308201d9a003020102020101300a06082a8648ce3d0403023059315730550603550403134e313339653339343065363462353439313732323038386439613064373431363238666338323665303934373564333431613738306163646533633462383037302e616e7977686572652d66696c65301e170d3730303130313030303030305a170d3731303230323030303030305a3059315730550603550403134e313339653339343065363462353439313732323038386439613064373431363238666338323665303934373564333431613738306163646533633462383037302e616e7977686572652d66696c653059301306072a8648ce3d020106082a8648ce3d0301070342000494f967df2f12b3a882b6bde7cb9c09dd2d4d8d02307fe63b98d87329ba6e5043e0ab11297787a07d42f4f417975e757ac643989dd128990fb7d5c7a6bf484633a3819130818e300e0603551d0f0101ff04040302078030130603551d25040c300a06082b06010505070301300c0603551d130101ff0402300030590603551d1104523050824e313339653339343065363462353439313732323038386439613064373431363238666338323665303934373564333431613738306163646533633462383037302e616e7977686572652d66696c65300a06082a8648ce3d0403020348003045022032e5fed4eb89332ee74571cf3419131f14b99d9e07759b3817fe87bf7c7c630d022100a989b71bf5ec6714cfa01a143055d527b1c9fd79b7c2b3a0984198215f7ef703"
    }

    private fun certificate(hex: String): X509Certificate {
        val der = ByteArray(hex.length / 2) { hex.substring(it * 2, it * 2 + 2).toInt(16).toByte() }
        return CertificateFactory.getInstance("X.509").generateCertificate(ByteArrayInputStream(der)) as X509Certificate
    }

    @Test
    fun theAgentsOwnProofIsAccepted() {
        verifyDeviceProof(DEVICE_ID, PUBLIC_KEY, PROOF, certificate(CERT))
    }

    // #127's acceptance. A PC can copy the document off the network; what it cannot do is
    // serve a certificate the device key has signed.
    @Test
    fun aCopiedDocumentOnAnotherPCIsRefused() {
        val e = assertFailsWith<WrongDevice> {
            verifyDeviceProof(DEVICE_ID, PUBLIC_KEY, PROOF, certificate(OTHER_CERT))
        }
        assertEquals(true, e.message?.contains("different PC"), e.message)
    }

    // The cheaper lie is a key that is not the device being looked for, and it fails before
    // the signature is looked at.
    @Test
    fun aKeyThatIsNotTheDeviceIsRefused() {
        assertFailsWith<WrongDevice> {
            verifyDeviceProof("a".repeat(64), PUBLIC_KEY, PROOF, certificate(CERT))
        }
    }

    @Test
    fun rubbishIsRefused() {
        assertFailsWith<WrongDevice>("no key and no proof") {
            verifyDeviceProof(DEVICE_ID, "", "", certificate(CERT))
        }
        assertFailsWith<WrongDevice>("a key that is not hexadecimal") {
            verifyDeviceProof(DEVICE_ID, "z".repeat(64), PROOF, certificate(CERT))
        }
        assertFailsWith<WrongDevice>("a proof that is not hexadecimal") {
            verifyDeviceProof(DEVICE_ID, PUBLIC_KEY, "zz", certificate(CERT))
        }
        assertFailsWith<WrongDevice>("a signature of the wrong length") {
            verifyDeviceProof(DEVICE_ID, PUBLIC_KEY, PROOF.dropLast(2), certificate(CERT))
        }
    }

    // What a person compares against the PC in front of them, and the agent prints the same
    // string.
    @Test
    fun theFingerprintIsTheOneTheAgentPrints() {
        assertEquals(FINGERPRINT, fingerprintOf(DEVICE_ID))
    }
}
