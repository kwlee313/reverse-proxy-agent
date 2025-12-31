package com.rpa.android

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.Service
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Binder
import android.os.Build
import android.os.IBinder
import androidx.core.app.NotificationCompat
import androidx.core.content.ContextCompat
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.launch
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow

class RpaService : Service() {
    private val binder = LocalBinder()
    private val statusFlow = MutableStateFlow(ServiceStatus())
    private val serviceScope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private var tunnelJob: Job? = null
    private lateinit var tunnelManager: SshTunnelManager
    private lateinit var networkMonitor: NetworkMonitor
    private var sleepMonitor: SleepMonitor? = null

    override fun onCreate() {
        super.onCreate()
        ServiceEvents.init(this)
        createNotificationChannel()
        updateStatus(ServiceState.CONNECTING, "Starting tunnel")
        if (!canPostNotifications()) {
            ServiceEvents.log("ERROR", "notification permission not granted")
            updateStatus(ServiceState.STOPPED, "Notification permission required")
            stopSelf()
            return
        }
        startForeground(NOTIFICATION_ID, buildNotification(statusFlow.value))
        val knownHosts = java.io.File(filesDir, "known_hosts")
        tunnelManager = SshTunnelManager(
            knownHostsFile = knownHosts,
            statusCallback = { state, detail -> updateStatus(state, detail) },
            logCallback = { level, message -> ServiceEvents.log(level, message) }
        )
        networkMonitor = NetworkMonitor(this) { reason ->
            ServiceEvents.log("INFO", reason)
            tunnelManager.requestRestart("network change")
        }
        networkMonitor.register()
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        val action = intent?.action
        if (action == ACTION_STOP) {
            stopSelf()
            return START_NOT_STICKY
        }
        startTunnel()
        return START_STICKY
    }

    override fun onDestroy() {
        tunnelJob?.cancel()
        serviceScope.coroutineContext.cancel()
        networkMonitor.unregister()
        sleepMonitor?.stop()
        updateStatus(ServiceState.STOPPED, "Service stopped")
        super.onDestroy()
    }

    override fun onBind(intent: Intent?): IBinder {
        return binder
    }

    fun statusFlow(): StateFlow<ServiceStatus> = statusFlow

    private fun updateStatus(state: ServiceState, detail: String) {
        statusFlow.value = statusFlow.value.copy(state = state, detail = detail)
        ServiceEvents.updateStatus(statusFlow.value)
        MetricsStore.onState(state)
        updateNotification()
    }

    private fun startTunnel() {
        if (tunnelJob?.isActive == true) {
            return
        }
        tunnelJob = serviceScope.launch {
            val configText = ConfigStore.loadText(this@RpaService)
            val config = runCatching { ConfigStore.parse(configText) }.getOrElse {
                ServiceEvents.log("ERROR", "config error: ${it.message}")
                updateStatus(ServiceState.STOPPED, "Config error: ${it.message}")
                stopSelf()
                return@launch
            }
            val keyPair = runCatching { KeyStore.loadKeyPair(this@RpaService) }.getOrElse {
                ServiceEvents.log("ERROR", "key error: ${it.message}")
                updateStatus(ServiceState.STOPPED, "Key error: ${it.message}")
                stopSelf()
                return@launch
            }
            MetricsStore.onUptimeStart()
            sleepMonitor?.stop()
            sleepMonitor = SleepMonitor(
                scope = serviceScope,
                intervalMs = config.client.sleepCheckSec.toLong() * 1000,
                gapMs = config.client.sleepGapSec.toLong() * 1000
            ) { reason ->
                ServiceEvents.log("INFO", reason)
                tunnelManager.requestRestart(reason)
            }
            sleepMonitor?.start()
            tunnelManager.run(config, keyPair)
        }
    }

    private fun updateNotification() {
        if (!canPostNotifications()) {
            return
        }
        val manager = getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        manager.notify(NOTIFICATION_ID, buildNotification(statusFlow.value))
    }

    private fun buildNotification(status: ServiceStatus): Notification {
        val stopIntent = Intent(this, RpaService::class.java).apply { action = ACTION_STOP }
        val stopPending = PendingIntentHelper.service(this, 1, stopIntent)

        return NotificationCompat.Builder(this, NOTIFICATION_CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_launcher)
            .setContentTitle("rpa tunnel")
            .setContentText("${status.state.label}: ${status.detail}")
            .setOngoing(true)
            .addAction(0, "Stop", stopPending)
            .build()
    }

    private fun createNotificationChannel() {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) {
            return
        }
        val manager = getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        val channel = NotificationChannel(
            NOTIFICATION_CHANNEL_ID,
            "rpa tunnel",
            NotificationManager.IMPORTANCE_LOW
        )
        manager.createNotificationChannel(channel)
    }

    private fun canPostNotifications(): Boolean {
        return if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU) {
            ContextCompat.checkSelfPermission(this, android.Manifest.permission.POST_NOTIFICATIONS) ==
                PackageManager.PERMISSION_GRANTED
        } else {
            true
        }
    }

    inner class LocalBinder : Binder() {
        fun service(): RpaService = this@RpaService
    }

    companion object {
        const val NOTIFICATION_CHANNEL_ID = "rpa_tunnel"
        const val NOTIFICATION_ID = 1001
        const val ACTION_STOP = "com.rpa.android.ACTION_STOP"
    }
}

data class ServiceStatus(
    val state: ServiceState = ServiceState.STOPPED,
    val detail: String = "Idle"
)

enum class ServiceState(val label: String) {
    STOPPED("STOPPED"),
    CONNECTING("CONNECTING"),
    RUNNING("RUNNING"),
}
