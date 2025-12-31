package com.rpa.android

import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.animation.AnimatedVisibility
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Button
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.Card
import androidx.compose.material3.CardDefaults
import androidx.compose.material3.ExperimentalMaterial3Api
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Icon
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.NavigationBar
import androidx.compose.material3.NavigationBarItem
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Scaffold
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TopAppBar
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.shadow
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import com.rpa.android.ui.theme.RpaTheme

class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContent {
            RpaTheme {
                Surface(modifier = Modifier.fillMaxSize(), color = MaterialTheme.colorScheme.background) {
                    RpaApp()
                }
            }
        }
    }
}

enum class TunnelState {
    STOPPED,
    CONNECTING,
    RUNNING,
}

data class StatusSnapshot(
    val state: TunnelState,
    val summary: String,
    val lastExit: String,
    val lastClass: String,
    val lastTrigger: String,
    val lastSuccessUtc: String,
    val tcpCheck: String,
    val tcpCheckError: String,
)

data class LogLine(
    val timestamp: String,
    val level: String,
    val message: String,
)

data class MetricItem(
    val key: String,
    val value: String,
)

data class DoctorItem(
    val title: String,
    val status: String,
    val detail: String,
)

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun RpaApp() {
    var selectedTab by remember { mutableStateOf(AppTab.Home) }
    val context = LocalContext.current
    val serviceStatus by ServiceEvents.status.collectAsState()
    val logs by ServiceEvents.logs.collectAsState()
    val metricsSnapshot by MetricsStore.metrics.collectAsState()
    val metrics = metricsSnapshot.toItems()
    val doctorItems = remember { mutableStateOf<List<DoctorItem>>(emptyList()) }
    val snapshot = remember(serviceStatus) {
        StatusSnapshot(
            state = when (serviceStatus.state) {
                ServiceState.RUNNING -> TunnelState.RUNNING
                ServiceState.CONNECTING -> TunnelState.CONNECTING
                ServiceState.STOPPED -> TunnelState.STOPPED
            },
            summary = "local forwards active",
            lastExit = metricsSnapshot.lastExit,
            lastClass = metricsSnapshot.lastClass,
            lastTrigger = metricsSnapshot.lastTrigger,
            lastSuccessUtc = metricsSnapshot.lastSuccessUnix?.toString() ?: "-",
            tcpCheck = "-",
            tcpCheckError = ""
        )
    }
    val quickNotes = remember(serviceStatus, metricsSnapshot) {
        listOf(
            "Service: ${serviceStatus.state.label}",
            serviceStatus.detail,
            "Backoff: ${metricsSnapshot.backoffMs ?: 0} ms"
        )
    }

    LaunchedEffect(serviceStatus.state, metricsSnapshot.lastExit) {
        doctorItems.value = DoctorChecks.run(context)
    }

    LaunchedEffect(Unit) {
        ServiceEvents.init(context)
    }

    AppBackground {
        Scaffold(
            containerColor = Color.Transparent,
            topBar = {
                TopAppBar(
                    title = {
                        Column {
                            Text(text = "rpa client", fontWeight = FontWeight.SemiBold)
                            Text(
                                text = "Foreground tunnel companion",
                                style = MaterialTheme.typography.labelSmall,
                                color = MaterialTheme.colorScheme.onSurfaceVariant
                            )
                        }
                    }
                )
            },
            bottomBar = {
                NavigationBar {
                    AppTab.values().forEach { tab ->
                        NavigationBarItem(
                            selected = tab == selectedTab,
                            onClick = { selectedTab = tab },
                            icon = { Icon(painterResource(id = tab.iconRes), contentDescription = null) },
                            label = { Text(tab.label) }
                        )
                    }
                }
            }
        ) { padding ->
            when (selectedTab) {
                AppTab.Home -> HomeScreen(
                    padding,
                    AppUiState(
                        snapshot = snapshot,
                        logs = logs,
                        metrics = metrics,
                        doctorItems = doctorItems.value,
                        configText = "",
                        quickNotes = quickNotes
                    ),
                    onStart = { RpaServiceController.start(context) },
                    onStop = { RpaServiceController.stop(context) }
                )
                AppTab.Logs -> LogsScreen(padding, logs)
                AppTab.Config -> ConfigScreen(padding)
                AppTab.Metrics -> MetricsScreen(padding, metrics)
                AppTab.Doctor -> DoctorScreen(padding, doctorItems.value)
            }
        }
    }
}

enum class AppTab(val label: String, val iconRes: Int) {
    Home("Status", R.drawable.ic_tab_status),
    Logs("Logs", R.drawable.ic_tab_logs),
    Config("Config", R.drawable.ic_tab_config),
    Metrics("Metrics", R.drawable.ic_tab_metrics),
    Doctor("Doctor", R.drawable.ic_tab_doctor),
}

@Composable
fun HomeScreen(
    padding: PaddingValues,
    state: AppUiState,
    onStart: () -> Unit,
    onStop: () -> Unit
) {
    LazyColumn(
        modifier = Modifier
            .fillMaxSize()
            .padding(padding)
            .padding(horizontal = 16.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp)
    ) {
        item {
            Spacer(modifier = Modifier.height(8.dp))
        }
        item {
            StatusHeader(state.snapshot)
        }
        item {
            ActionRow(
                isRunning = state.snapshot.state == TunnelState.RUNNING,
                onStart = onStart,
                onStop = onStop
            )
        }
        item {
            StatusDetails(state.snapshot)
        }
        item {
            QuickNotes(state.quickNotes)
        }
    }
}

@Composable
fun StatusHeader(snapshot: StatusSnapshot) {
    val stateColor = when (snapshot.state) {
        TunnelState.RUNNING -> Color(0xFF2E7D32)
        TunnelState.CONNECTING -> Color(0xFFF9A825)
        TunnelState.STOPPED -> Color(0xFFC62828)
    }
    val pulseAlpha by animateFloatAsState(
        targetValue = if (snapshot.state == TunnelState.CONNECTING) 0.6f else 1f,
        label = "pulse"
    )
    Card(
        colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surfaceVariant),
        modifier = Modifier.shadow(4.dp, RoundedCornerShape(18.dp))
    ) {
        Column(modifier = Modifier.padding(16.dp)) {
            Text(text = "State", style = MaterialTheme.typography.labelLarge)
            Row(verticalAlignment = Alignment.CenterVertically) {
                Box(
                    modifier = Modifier
                        .size(10.dp)
                        .background(stateColor.copy(alpha = pulseAlpha), RoundedCornerShape(50))
                )
                Spacer(modifier = Modifier.size(8.dp))
                Text(
                    text = snapshot.state.name,
                    style = MaterialTheme.typography.headlineSmall.copy(fontWeight = FontWeight.SemiBold)
                )
            }
            Spacer(modifier = Modifier.height(6.dp))
            Text(text = snapshot.summary, style = MaterialTheme.typography.bodyMedium)
        }
    }
}

@Composable
fun ActionRow(isRunning: Boolean, onStart: () -> Unit, onStop: () -> Unit) {
    Row(horizontalArrangement = Arrangement.spacedBy(12.dp)) {
        Button(
            onClick = onStart,
            enabled = !isRunning,
            modifier = Modifier.weight(1f)
        ) {
            Text(text = "Start")
        }
        Button(
            onClick = onStop,
            enabled = isRunning,
            colors = ButtonDefaults.buttonColors(containerColor = MaterialTheme.colorScheme.error),
            modifier = Modifier.weight(1f)
        ) {
            Text(text = "Stop")
        }
    }
}

@Composable
fun StatusDetails(snapshot: StatusSnapshot) {
    Card(
        colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surface),
        modifier = Modifier.shadow(2.dp, RoundedCornerShape(16.dp))
    ) {
        Column(modifier = Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            DetailRow("Last success", snapshot.lastSuccessUtc)
            DetailRow("Last exit", snapshot.lastExit)
            DetailRow("Last class", snapshot.lastClass)
            DetailRow("Last trigger", snapshot.lastTrigger)
            DetailRow("TCP check", snapshot.tcpCheck)
            AnimatedVisibility(snapshot.tcpCheckError.isNotBlank()) {
                Text(
                    text = snapshot.tcpCheckError,
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.error
                )
            }
        }
    }
}

@Composable
fun DetailRow(label: String, value: String) {
    Column {
        Text(text = label, style = MaterialTheme.typography.labelMedium, color = MaterialTheme.colorScheme.primary)
        Text(text = value, style = MaterialTheme.typography.bodyMedium)
    }
}

@Composable
fun QuickNotes(notes: List<String>) {
    Card(colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surfaceVariant)) {
        Column(modifier = Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
            Text(text = "Notes", style = MaterialTheme.typography.labelLarge)
            notes.forEach { note ->
                Text(text = "• $note", style = MaterialTheme.typography.bodyMedium)
            }
        }
    }
}

@Composable
fun LogsScreen(padding: PaddingValues, logs: List<LogLine>) {
    var query by remember { mutableStateOf("") }
    val filtered = logs.filter { it.message.contains(query, ignoreCase = true) }

    Column(
        modifier = Modifier
            .fillMaxSize()
            .padding(padding)
            .padding(horizontal = 16.dp)
    ) {
        Spacer(modifier = Modifier.height(12.dp))
        OutlinedTextField(
            value = query,
            onValueChange = { query = it },
            label = { Text("Search logs") },
            modifier = Modifier.fillMaxWidth()
        )
        Spacer(modifier = Modifier.height(12.dp))
        LazyColumn(verticalArrangement = Arrangement.spacedBy(8.dp)) {
            items(filtered) { log ->
                Card(
                    modifier = Modifier.fillMaxWidth(),
                    colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surface)
                ) {
                    Column(modifier = Modifier.padding(12.dp)) {
                        Text(text = "${log.timestamp}  ${log.level}", style = MaterialTheme.typography.labelSmall)
                        Text(text = log.message, style = MaterialTheme.typography.bodyMedium)
                    }
                }
            }
        }
    }
}

@Composable
fun ConfigScreen(padding: PaddingValues) {
    val context = LocalContext.current
    var editorText by remember { mutableStateOf(ConfigStore.loadText(context)) }
    var showHint by remember { mutableStateOf(true) }
    val keyInfo = remember { KeyStore.ensureKeyPair(context) }
    val keyError = keyInfo.error
    val keyText = if (keyError.isNullOrBlank()) {
        keyInfo.publicKey
    } else {
        "Key generation failed: $keyError"
    }
    val keyActionsEnabled = keyError.isNullOrBlank() && keyInfo.publicKey.isNotBlank()

    Column(
        modifier = Modifier
            .fillMaxSize()
            .padding(padding)
            .padding(horizontal = 16.dp)
    ) {
        Spacer(modifier = Modifier.height(12.dp))
        Text(text = "Config (YAML)", style = MaterialTheme.typography.labelLarge)
        Spacer(modifier = Modifier.height(6.dp))
        OutlinedTextField(
            value = editorText,
            onValueChange = { editorText = it },
            modifier = Modifier
                .fillMaxWidth()
                .weight(1f),
            textStyle = MaterialTheme.typography.bodySmall,
            minLines = 12
        )
        Spacer(modifier = Modifier.height(8.dp))
        AnimatedVisibility(showHint) {
            Text(
                text = "Changes are applied on save. You can paste a full rpa.yaml here.",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant
            )
        }
        Spacer(modifier = Modifier.height(12.dp))
        Row(horizontalArrangement = Arrangement.spacedBy(12.dp)) {
            Button(
                onClick = {
                    runCatching { ConfigStore.parse(editorText) }
                        .onSuccess { ShareHelper.toast(context, "Config valid") }
                        .onFailure { ShareHelper.toast(context, it.message ?: "Config invalid") }
                },
                modifier = Modifier.weight(1f)
            ) {
                Text(text = "Validate")
            }
            Button(
                onClick = {
                    ConfigStore.saveText(context, editorText)
                    showHint = false
                    ShareHelper.toast(context, "Saved")
                },
                modifier = Modifier.weight(1f)
            ) {
                Text(text = "Save")
            }
        }
        Spacer(modifier = Modifier.height(12.dp))
        HorizontalDivider()
        Spacer(modifier = Modifier.height(12.dp))
        Text(text = "SSH public key", style = MaterialTheme.typography.labelLarge)
        Spacer(modifier = Modifier.height(6.dp))
        OutlinedTextField(
            value = keyText,
            onValueChange = {},
            modifier = Modifier.fillMaxWidth(),
            readOnly = true,
            textStyle = MaterialTheme.typography.bodySmall,
            minLines = 3
        )
        Spacer(modifier = Modifier.height(8.dp))
        if (!keyError.isNullOrBlank()) {
            Text(
                text = "Key generation failed on this device. Check Logs for details.",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.error
            )
            Spacer(modifier = Modifier.height(8.dp))
        }
        Row(horizontalArrangement = Arrangement.spacedBy(12.dp)) {
            Button(
                onClick = { ShareHelper.copy(context, "rpa public key", keyInfo.publicKey) },
                modifier = Modifier.weight(1f),
                enabled = keyActionsEnabled
            ) {
                Text(text = "Copy")
            }
            Button(
                onClick = { ShareHelper.share(context, "rpa public key", keyInfo.publicKey) },
                modifier = Modifier.weight(1f),
                enabled = keyActionsEnabled
            ) {
                Text(text = "Share")
            }
        }
        Spacer(modifier = Modifier.height(12.dp))
    }
}

@Composable
fun MetricsScreen(padding: PaddingValues, metrics: List<MetricItem>) {
    LazyColumn(
        modifier = Modifier
            .fillMaxSize()
            .padding(padding)
            .padding(horizontal = 16.dp),
        verticalArrangement = Arrangement.spacedBy(8.dp)
    ) {
        item { Spacer(modifier = Modifier.height(12.dp)) }
        items(metrics) { metric ->
            Card(colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surface)) {
                Row(
                    modifier = Modifier
                        .fillMaxWidth()
                        .padding(12.dp),
                    horizontalArrangement = Arrangement.SpaceBetween
                ) {
                    Text(text = metric.key, style = MaterialTheme.typography.bodyMedium)
                    Text(text = metric.value, style = MaterialTheme.typography.labelLarge)
                }
            }
        }
    }
}

@Composable
fun DoctorScreen(padding: PaddingValues, items: List<DoctorItem>) {
    LazyColumn(
        modifier = Modifier
            .fillMaxSize()
            .padding(padding)
            .padding(horizontal = 16.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp)
    ) {
        item { Spacer(modifier = Modifier.height(12.dp)) }
        items(items) { item ->
            Card(
                colors = CardDefaults.cardColors(containerColor = MaterialTheme.colorScheme.surfaceVariant)
            ) {
                Column(modifier = Modifier.padding(16.dp)) {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Text(
                            text = item.title,
                            style = MaterialTheme.typography.titleMedium,
                            modifier = Modifier.weight(1f),
                            maxLines = 1,
                            overflow = TextOverflow.Ellipsis
                        )
                        StatusPill(item.status)
                    }
                    Spacer(modifier = Modifier.height(6.dp))
                    Text(text = item.detail, style = MaterialTheme.typography.bodySmall)
                }
            }
        }
    }
}

@Composable
fun StatusPill(label: String) {
    val background = when (label) {
        "OK" -> Color(0xFF1B5E20)
        "WARN" -> Color(0xFFF57F17)
        else -> Color(0xFFB71C1C)
    }
    Box(
        modifier = Modifier
            .background(background, RoundedCornerShape(50))
            .padding(horizontal = 8.dp, vertical = 2.dp)
    ) {
        Text(text = label, color = Color.White, style = MaterialTheme.typography.labelSmall)
    }
}

@Composable
fun AppBackground(content: @Composable () -> Unit) {
    Box(
        modifier = Modifier
            .fillMaxSize()
            .background(
                Brush.linearGradient(
                    listOf(
                        Color(0xFFFFFBEB),
                        Color(0xFFF8FAFC),
                        Color(0xFFF1F5F9)
                    )
                )
            )
    ) {
        Box(
            modifier = Modifier
                .offset(x = 180.dp, y = (-60).dp)
                .size(220.dp)
                .background(
                    Brush.radialGradient(
                        colors = listOf(Color(0x5522D3EE), Color.Transparent)
                    ),
                    RoundedCornerShape(200.dp)
                )
        )
        Box(
            modifier = Modifier
                .offset(x = (-40).dp, y = 420.dp)
                .size(260.dp)
                .background(
                    Brush.radialGradient(
                        colors = listOf(Color(0x33F97316), Color.Transparent)
                    ),
                    RoundedCornerShape(200.dp)
                )
        )
        content()
    }
}

data class AppUiState(
    val snapshot: StatusSnapshot,
    val logs: List<LogLine>,
    val metrics: List<MetricItem>,
    val doctorItems: List<DoctorItem>,
    val configText: String,
    val quickNotes: List<String>,
)
