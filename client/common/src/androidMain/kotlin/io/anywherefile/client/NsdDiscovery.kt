package io.anywherefile.client

import android.content.Context
import android.net.nsd.DiscoveryRequest
import android.net.nsd.NsdManager
import android.net.nsd.NsdServiceInfo
import android.os.Build
import java.util.concurrent.Executors

// The Android browse, through the system's own resolver. Nothing here opens a socket, so
// the local network permission that Android 17 asks for (#98) is checked by the caller
// before start, and pick is the way around it: the system shows the PCs and hands over
// the one the person chose.
class NsdDiscovery(context: Context, private val devices: Devices) : Discovery {
    private val nsd = context.getSystemService(NsdManager::class.java)
    private val executor = Executors.newSingleThreadExecutor()
    private val ids = HashMap<String, String>() // instance name to device id

    private val listener = object : NsdManager.DiscoveryListener {
        override fun onServiceFound(service: NsdServiceInfo) {
            // A found record has a name and nothing else; resolving fills in TXT and SRV.
            // One listener per resolve, because the resolver refuses to reuse a busy one.
            @Suppress("DEPRECATION")
            nsd.resolveService(service, object : NsdManager.ResolveListener {
                override fun onServiceResolved(info: NsdServiceInfo) = resolved(info)
                override fun onResolveFailed(info: NsdServiceInfo, errorCode: Int) {}
            })
        }

        override fun onServiceLost(service: NsdServiceInfo) {
            synchronized(ids) { ids.remove(service.serviceName) }?.let(devices::lost)
        }

        override fun onDiscoveryStarted(serviceType: String) {}
        override fun onDiscoveryStopped(serviceType: String) {}
        override fun onStartDiscoveryFailed(serviceType: String, errorCode: Int) {}
        override fun onStopDiscoveryFailed(serviceType: String, errorCode: Int) {}
    }

    override fun start() = nsd.discoverServices(SERVICE_TYPE, NsdManager.PROTOCOL_DNS_SD, listener)

    override fun stop() {
        try {
            nsd.stopServiceDiscovery(listener)
        } catch (e: IllegalArgumentException) {
            // Never started, or already stopped.
        }
    }

    // pick asks the system to show the PCs on the network and hand over the one the person
    // chose, resolved. It needs no permission, which is why it exists (Android 17, #98).
    fun pick() {
        if (Build.VERSION.SDK_INT < 37) return
        val request = DiscoveryRequest.Builder(SERVICE_TYPE).setFlags(DiscoveryRequest.FLAG_SHOW_PICKER).build()
        nsd.registerServiceInfoCallback(request, executor, object : NsdManager.ServiceInfoCallback {
            override fun onServiceUpdated(info: NsdServiceInfo) = resolved(info)
            override fun onServiceLost() {}
            override fun onServiceInfoCallbackRegistrationFailed(errorCode: Int) {}
            override fun onServiceInfoCallbackUnregistered() {}
        })
    }

    private fun resolved(info: NsdServiceInfo) {
        val txt = info.attributes.mapValues { (_, value) -> value?.decodeToString().orEmpty() }
        val host = (if (Build.VERSION.SDK_INT >= 34) info.hostAddresses.firstOrNull() else @Suppress("DEPRECATION") info.host)
            ?.hostAddress ?: return
        val device = deviceFrom(txt, host, info.port) ?: return
        synchronized(ids) { ids[info.serviceName] = device.id }
        devices.seen(device)
        confirm(device, devices)
    }
}
