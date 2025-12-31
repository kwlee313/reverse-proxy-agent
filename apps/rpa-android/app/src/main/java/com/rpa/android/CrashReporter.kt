package com.rpa.android

import android.content.Context
import java.io.File
import java.time.Instant
import java.time.ZoneId
import java.time.format.DateTimeFormatter

object CrashReporter {
    private const val LOG_FILE = "rpa.log"
    @Volatile
    private var installed = false
    private var previous: Thread.UncaughtExceptionHandler? = null
    private val formatter = DateTimeFormatter.ofPattern("yyyy-MM-dd HH:mm:ss")
        .withZone(ZoneId.systemDefault())

    fun init(context: Context) {
        if (installed) {
            return
        }
        installed = true
        LogStore.init(context)
        val appContext = context.applicationContext
        previous = Thread.getDefaultUncaughtExceptionHandler()
        Thread.setDefaultUncaughtExceptionHandler { thread, throwable ->
            runCatching {
                val file = File(appContext.filesDir, LOG_FILE)
                val timestamp = formatter.format(Instant.now())
                val summary = buildString {
                    append(throwable.javaClass.name)
                    val message = throwable.message
                    if (!message.isNullOrBlank()) {
                        append(": ")
                        append(message)
                    }
                    throwable.stackTrace.firstOrNull()?.let { frame ->
                        append(" at ")
                        append(frame.className)
                        append(":")
                        append(frame.lineNumber)
                    }
                }
                file.appendText("$timestamp|CRASH|$summary\n")
            }
            previous?.uncaughtException(thread, throwable)
        }
    }
}
