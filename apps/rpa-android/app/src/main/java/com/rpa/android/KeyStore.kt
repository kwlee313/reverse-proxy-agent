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

    fun ensureKeyPair(context: Context): KeyPairInfo {
        val dir = File(context.filesDir, KEY_DIR)
        if (!dir.exists()) {
            dir.mkdirs()
        }
        val privateFile = File(dir, PRIVATE_KEY_FILE)
        val publicFile = File(dir, PUBLIC_KEY_FILE)
        val publicDerFile = File(dir, PUBLIC_KEY_DER_FILE)
        return try {
            if (privateFile.exists() && publicFile.exists() && publicDerFile.exists()) {
                return KeyPairInfo(
                    privateKeyPath = privateFile.absolutePath,
                    publicKey = publicFile.readText(),
                    exists = true,
                    error = null
                )
            }
            val pair = generateEd25519KeyPair()
            val publicKeyOpenSsh = toOpenSshPublicKey(pair.public)
            privateFile.writeBytes(pair.private.encoded)
            publicFile.writeText(publicKeyOpenSsh)
            publicDerFile.writeBytes(pair.public.encoded)
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
    }

    fun loadKeyPair(context: Context): KeyPair {
        val dir = File(context.filesDir, KEY_DIR)
        val privateFile = File(dir, PRIVATE_KEY_FILE)
        val publicDerFile = File(dir, PUBLIC_KEY_DER_FILE)
        if (!privateFile.exists() || !publicDerFile.exists()) {
            ensureKeyPair(context)
        }
        val privateBytes = privateFile.readBytes()
        val publicBytes = publicDerFile.readBytes()
        val keyFactory = java.security.KeyFactory.getInstance("Ed25519")
        val privateKey = keyFactory.generatePrivate(java.security.spec.PKCS8EncodedKeySpec(privateBytes))
        val publicKey = keyFactory.generatePublic(java.security.spec.X509EncodedKeySpec(publicBytes))
        return KeyPair(publicKey, privateKey)
    }

    private fun generateEd25519KeyPair(): KeyPair {
        val generator = KeyPairGenerator.getInstance("Ed25519")
        return generator.generateKeyPair()
    }

    private fun toOpenSshPublicKey(publicKey: PublicKey): String {
        val raw = publicKey.encoded
        val b64 = Base64.encodeToString(raw, Base64.NO_WRAP)
        return "ssh-ed25519 $b64 rpa-android"
    }
}

data class KeyPairInfo(
    val privateKeyPath: String,
    val publicKey: String,
    val exists: Boolean,
    val error: String?
)
