package com.rpa.android

import kotlinx.coroutines.delay
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.isActive
import net.schmizz.sshj.SSHClient
import net.schmizz.sshj.connection.channel.direct.LocalPortForwarder
import net.schmizz.sshj.connection.channel.direct.Parameters
import net.schmizz.sshj.transport.kex.DHG14
import net.schmizz.sshj.transport.kex.DHG14SHA256
import net.schmizz.sshj.transport.kex.DHG1
import java.io.IOException
import java.net.InetAddress
import java.net.InetSocketAddress
import java.net.ServerSocket
import java.security.KeyPair
import kotlin.math.min

class SshTunnelManager(
    private val knownHostsFile: java.io.File,
    private val statusCallback: (ServiceState, String) -> Unit,
    private val logCallback: (String, String) -> Unit
) {
    private var client: SSHClient? = null
    private val forwarders = mutableListOf<LocalPortForwarder>()
    private val sockets = mutableListOf<ServerSocket>()
    @Volatile
    private var restartRequested = false
    private var debounceMs: Long = 2000
    private var lastTriggerAtMs: Long = 0
    private var periodicRestartMs: Long = 0
    private var lastStartAtMs: Long = 0

    suspend fun run(config: RpaConfig, keyPair: KeyPair) {
        val backoff = Backoff(
            config.client.restart.minDelayMs.toLong(),
            config.client.restart.maxDelayMs.toLong(),
            config.client.restart.factor,
            config.client.restart.jitter
        )
        debounceMs = config.client.restart.debounceMs.toLong()
        periodicRestartMs = config.client.periodicRestartSec.toLong() * 1000
        while (currentCoroutineContext().isActive) {
            try {
                statusCallback(ServiceState.CONNECTING, "Connecting")
                MetricsStore.onStartAttempt()
                connect(config, keyPair)
                MetricsStore.onStartSuccess()
                MetricsStore.onLastSuccess()
                MetricsStore.onLastClass("clean")
                statusCallback(ServiceState.RUNNING, "Tunnel running")
                backoff.reset()
                lastStartAtMs = System.currentTimeMillis()
                while (currentCoroutineContext().isActive && client?.isConnected == true && !restartRequested) {
                    if (periodicRestartMs > 0 && System.currentTimeMillis() - lastStartAtMs >= periodicRestartMs) {
                        requestRestart("periodic")
                    }
                    delay(1000)
                }
                if (restartRequested) {
                    restartRequested = false
                    logCallback("WARN", "restart requested")
                }
                logCallback("WARN", "ssh disconnected")
                MetricsStore.onLastClass("network")
                MetricsStore.onExitSuccess()
                MetricsStore.onLastExit("ssh disconnected")
            } catch (e: Exception) {
                val className = ErrorClassifier.classify(e.message)
                logCallback("ERROR", "ssh error ($className): ${e.message}")
                MetricsStore.onLastClass(className)
                MetricsStore.onStartFailure()
                MetricsStore.onExitFailure()
                MetricsStore.onLastExit(e.message ?: "ssh error")
            } finally {
                closeForwarders()
                disconnect()
            }
            if (!currentCoroutineContext().isActive) {
                break
            }
            val delayMs = backoff.nextDelayMs()
            MetricsStore.onRestart()
            MetricsStore.onRestartScheduled(delayMs)
            MetricsStore.onLastTrigger("retry")
            statusCallback(ServiceState.CONNECTING, "Retry in ${delayMs / 1000}s")
            delay(delayMs)
        }
    }

    private fun connect(config: RpaConfig, keyPair: KeyPair) {
        val ssh = SSHClient()
        // Avoid X25519 on Android where BC provider may not support it.
        ssh.setKeyExchangeFactories(listOf(DHG14SHA256(), DHG14(), DHG1()))
        logCallback("INFO", "kex: diffie-hellman-group14-sha256, diffie-hellman-group14-sha1, diffie-hellman-group1-sha1")
        val hostVerifier = AcceptNewKnownHosts(knownHostsFile) { msg -> logCallback("WARN", msg) }
        ssh.addHostKeyVerifier(hostVerifier)
        ssh.setConnectTimeout(15000)
        ssh.setTimeout(30000)
        ssh.connect(config.ssh.host, config.ssh.port)
        val keyProvider = ssh.loadKeys(keyPair)
        ssh.authPublickey(config.ssh.user, keyProvider)
        client = ssh
        logCallback("INFO", "ssh connected")

        for (spec in config.client.localForwards) {
            val forward = parseForward(spec)
            val params = Parameters(forward.localHost, forward.localPort, forward.remoteHost, forward.remotePort)
            val socket = ServerSocket()
            socket.reuseAddress = true
            socket.bind(InetSocketAddress(InetAddress.getByName(forward.localHost), forward.localPort))
            val forwarder = ssh.newLocalPortForwarder(params, socket)
            val thread = Thread { runForwarder(forwarder) }
            thread.isDaemon = true
            thread.start()
            sockets.add(socket)
            forwarders.add(forwarder)
            logCallback("INFO", "forwarding ${forward.localHost}:${forward.localPort} -> ${forward.remoteHost}:${forward.remotePort}")
        }
    }

    private fun runForwarder(forwarder: LocalPortForwarder) {
        try {
            forwarder.listen()
        } catch (e: IOException) {
            logCallback("WARN", "forwarder stopped: ${e.message}")
        }
    }

    private fun closeForwarders() {
        forwarders.forEach { runCatching { it.close() } }
        forwarders.clear()
        sockets.forEach { runCatching { it.close() } }
        sockets.clear()
    }

    private fun disconnect() {
        client?.let { runCatching { it.disconnect() } }
        client = null
    }

    fun requestRestart(reason: String) {
        val now = System.currentTimeMillis()
        if (now-lastTriggerAtMs < debounceMs) {
            logCallback("INFO", "restart skipped: debounced")
            return
        }
        lastTriggerAtMs = now
        MetricsStore.onLastTrigger(reason)
        restartRequested = true
        client?.let { runCatching { it.disconnect() } }
    }
}

private data class ForwardSpec(
    val localHost: String,
    val localPort: Int,
    val remoteHost: String,
    val remotePort: Int
)

private fun parseForward(spec: String): ForwardSpec {
    val parts = spec.split(":").map { it.trim() }
    if (parts.size != 4) {
        throw IllegalArgumentException("invalid forward spec: $spec")
    }
    val localHost = parts[0].ifBlank { "127.0.0.1" }
    val localPort = parts[1].toInt()
    val remoteHost = parts[2].ifBlank { "127.0.0.1" }
    val remotePort = parts[3].toInt()
    return ForwardSpec(localHost, localPort, remoteHost, remotePort)
}

private class Backoff(
    private val minDelayMs: Long,
    private val maxDelayMs: Long,
    private val factor: Double,
    private val jitter: Double
) {
    private var currentDelay = minDelayMs

    fun nextDelayMs(): Long {
        val delay = applyJitter(currentDelay)
        currentDelay = min(maxDelayMs, (currentDelay * factor).toLong())
        return delay
    }

    fun reset() {
        currentDelay = minDelayMs
    }

    private fun applyJitter(value: Long): Long {
        if (jitter <= 0) {
            return value
        }
        val delta = value * jitter
        val minVal = value - delta
        val maxVal = value + delta
        val rand = kotlin.random.Random.nextDouble(minVal, maxVal)
        return rand.toLong().coerceAtLeast(0)
    }
}
