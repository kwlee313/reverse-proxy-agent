package com.rpa.android

import android.content.Context
import android.util.Base64
import java.io.File
import java.security.KeyPair
import java.security.KeyPairGenerator
import java.security.PublicKey

object KeyStore {
    private const val KEY_DIR = "keys"
    private const val PRIVATE_KEY_FILE = "rpa_ed25519"
    private const val PUBLIC_KEY_FILE = "rpa_ed25519.pub"
    private const val PUBLIC_KEY_DER_FILE = "rpa_ed25519.pub.der"
    private const val ALGORITHM_FILE = "rpa_ed25519.alg"
    private const val ALG_ED25519 = "ed25519"
    private const val ALG_RSA = "rsa"

    fun ensureKeyPair(context: Context): KeyPairInfo {
        val dir = File(context.filesDir, KEY_DIR)
        if (!dir.exists()) {
            dir.mkdirs()
        }
        val privateFile = File(dir, PRIVATE_KEY_FILE)
        val publicFile = File(dir, PUBLIC_KEY_FILE)
        val publicDerFile = File(dir, PUBLIC_KEY_DER_FILE)
        val algFile = File(dir, ALGORITHM_FILE)
        return try {
            if (privateFile.exists() && publicFile.exists() && publicDerFile.exists()) {
                return KeyPairInfo(
                    privateKeyPath = privateFile.absolutePath,
                    publicKey = publicFile.readText(),
                    exists = true,
                    error = null
                )
            }
            val algorithm = selectAlgorithm()
            val pair = generateKeyPair(algorithm)
            val publicKeyOpenSsh = toOpenSshPublicKey(pair.public, algorithm)
            privateFile.writeBytes(pair.private.encoded)
            publicFile.writeText(publicKeyOpenSsh)
            publicDerFile.writeBytes(pair.public.encoded)
            algFile.writeText(algorithm)
            KeyPairInfo(
                privateKeyPath = privateFile.absolutePath,
                publicKey = publicKeyOpenSsh,
                exists = false,
                error = null
            )
        } catch (e: Exception) {
            ServiceEvents.log("ERROR", "Key generation failed: ${e.message ?: "unknown error"}")
            KeyPairInfo(
                privateKeyPath = privateFile.absolutePath,
                publicKey = "",
                exists = false,
                error = e.message ?: "unknown error"
            )
        }
    }

    fun getPublicKey(context: Context): String? {
        val file = File(File(context.filesDir, KEY_DIR), PUBLIC_KEY_FILE)
        if (!file.exists()) {
            return null
        }
        return file.readText()
    }

    fun resetKeys(context: Context) {
        val dir = File(context.filesDir, KEY_DIR)
        if (!dir.exists()) {
            return
        }
        File(dir, PRIVATE_KEY_FILE).delete()
        File(dir, PUBLIC_KEY_FILE).delete()
        File(dir, PUBLIC_KEY_DER_FILE).delete()
        File(dir, ALGORITHM_FILE).delete()
    }

    fun loadKeyPair(context: Context): KeyPair {
        val dir = File(context.filesDir, KEY_DIR)
        val privateFile = File(dir, PRIVATE_KEY_FILE)
        val publicDerFile = File(dir, PUBLIC_KEY_DER_FILE)
        val algFile = File(dir, ALGORITHM_FILE)
        if (!privateFile.exists() || !publicDerFile.exists()) {
            ensureKeyPair(context)
        }
        val algorithm = readAlgorithm(algFile, File(dir, PUBLIC_KEY_FILE))
        val privateBytes = privateFile.readBytes()
        val publicBytes = publicDerFile.readBytes()
        return try {
            val keyFactory = java.security.KeyFactory.getInstance(toJcaAlgorithm(algorithm))
            val privateKey = keyFactory.generatePrivate(java.security.spec.PKCS8EncodedKeySpec(privateBytes))
            val publicKey = keyFactory.generatePublic(java.security.spec.X509EncodedKeySpec(publicBytes))
            KeyPair(publicKey, privateKey)
        } catch (e: Exception) {
            ServiceEvents.log("WARN", "Key load failed for $algorithm, regenerating RSA: ${e.message}")
            regenerateKeys(context, ALG_RSA)
            val keyFactory = java.security.KeyFactory.getInstance("RSA")
            val refreshedPrivate = privateFile.readBytes()
            val refreshedPublic = publicDerFile.readBytes()
            val privateKey = keyFactory.generatePrivate(java.security.spec.PKCS8EncodedKeySpec(refreshedPrivate))
            val publicKey = keyFactory.generatePublic(java.security.spec.X509EncodedKeySpec(refreshedPublic))
            KeyPair(publicKey, privateKey)
        }
    }

    private fun selectAlgorithm(): String {
        return if (supportsEd25519()) ALG_ED25519 else ALG_RSA
    }

    private fun supportsEd25519(): Boolean {
        return runCatching { KeyPairGenerator.getInstance("Ed25519") }.isSuccess
    }

    private fun generateKeyPair(algorithm: String): KeyPair {
        return if (algorithm == ALG_ED25519) {
            KeyPairGenerator.getInstance("Ed25519").generateKeyPair()
        } else {
            val generator = KeyPairGenerator.getInstance("RSA")
            generator.initialize(2048)
            generator.generateKeyPair()
        }
    }

    private fun toOpenSshPublicKey(publicKey: PublicKey, algorithm: String): String {
        return if (algorithm == ALG_ED25519) {
            val raw = extractEd25519PublicKey(publicKey.encoded)
            val payload = buildSshPayload("ssh-ed25519", raw)
            "ssh-ed25519 ${Base64.encodeToString(payload, Base64.NO_WRAP)} rpa-android"
        } else {
            val rsa = publicKey as java.security.interfaces.RSAPublicKey
            val payload = buildSshPayload("ssh-rsa", rsa.publicExponent.toByteArray(), rsa.modulus.toByteArray())
            "ssh-rsa ${Base64.encodeToString(payload, Base64.NO_WRAP)} rpa-android"
        }
    }

    private fun buildSshPayload(type: String, vararg fields: ByteArray): ByteArray {
        val out = java.io.ByteArrayOutputStream()
        writeString(out, type.toByteArray(Charsets.US_ASCII))
        fields.forEach { writeString(out, it) }
        return out.toByteArray()
    }

    private fun writeString(out: java.io.ByteArrayOutputStream, value: ByteArray) {
        val len = value.size
        out.write(byteArrayOf(
            (len ushr 24).toByte(),
            (len ushr 16).toByte(),
            (len ushr 8).toByte(),
            len.toByte()
        ))
        out.write(value)
    }

    private fun extractEd25519PublicKey(encoded: ByteArray): ByteArray {
        if (encoded.isNotEmpty() && encoded.lastIndex >= 32) {
            return encoded.copyOfRange(encoded.size - 32, encoded.size)
        }
        throw IllegalArgumentException("invalid ed25519 public key")
    }

    private fun toJcaAlgorithm(algorithm: String): String {
        return if (algorithm == ALG_ED25519) "Ed25519" else "RSA"
    }

    private fun readAlgorithm(algFile: File, publicFile: File): String {
        if (algFile.exists()) {
            return algFile.readText().trim().ifBlank { ALG_RSA }
        }
        if (publicFile.exists()) {
            val first = publicFile.readText().trim().split(" ").firstOrNull().orEmpty()
            if (first == "ssh-ed25519") return ALG_ED25519
            if (first == "ssh-rsa") return ALG_RSA
        }
        return ALG_RSA
    }

    private fun regenerateKeys(context: Context, algorithm: String) {
        val dir = File(context.filesDir, KEY_DIR)
        if (!dir.exists()) {
            dir.mkdirs()
        }
        val privateFile = File(dir, PRIVATE_KEY_FILE)
        val publicFile = File(dir, PUBLIC_KEY_FILE)
        val publicDerFile = File(dir, PUBLIC_KEY_DER_FILE)
        val algFile = File(dir, ALGORITHM_FILE)
        val pair = generateKeyPair(algorithm)
        val publicKeyOpenSsh = toOpenSshPublicKey(pair.public, algorithm)
        privateFile.writeBytes(pair.private.encoded)
        publicFile.writeText(publicKeyOpenSsh)
        publicDerFile.writeBytes(pair.public.encoded)
        algFile.writeText(algorithm)
    }
}

data class KeyPairInfo(
    val privateKeyPath: String,
    val publicKey: String,
    val exists: Boolean,
    val error: String?
)
