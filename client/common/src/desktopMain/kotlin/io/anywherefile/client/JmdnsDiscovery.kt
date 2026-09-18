package io.anywherefile.client

import javax.jmdns.JmDNS
import javax.jmdns.ServiceEvent
import javax.jmdns.ServiceListener
import kotlin.concurrent.thread

// The desktop browse, through jmdns. Windows, macOS and Linux each run their own mDNS
// responder on 5353; jmdns binds beside it with address reuse, the same way the agent does.
class JmdnsDiscovery(private val devices: Devices) : Discovery {
    @Volatile private var jmdns: JmDNS? = null
    private val ids = HashMap<String, String>() // instance name to device id

    private val listener = object : ServiceListener {
        override fun serviceAdded(event: ServiceEvent) {
            // Added means seen, not read. Asking for the info is what fills in TXT and SRV.
            event.dns.requestServiceInfo(event.type, event.name)
        }

        override fun serviceRemoved(event: ServiceEvent) {
            synchronized(ids) { ids.remove(event.name) }?.let(devices::lost)
        }

        override fun serviceResolved(event: ServiceEvent) {
            val info = event.info
            val txt = info.propertyNames.toList().associateWith { info.getPropertyString(it).orEmpty() }
            val host = info.inet4Addresses.firstOrNull()?.hostAddress ?: return
            val device = deviceFrom(txt, host, info.port) ?: return
            synchronized(ids) { ids[event.name] = device.id }
            devices.seen(device)
            confirm(device, devices)
        }
    }

    override fun start() {
        // Creating the instance binds the socket and can take a moment; off the UI thread.
        thread(isDaemon = true, name = "jmdns") {
            val dns = JmDNS.create()
            jmdns = dns
            // Which interface the browse is on. A PC with several (a VPN, a container
            // bridge) can end up browsing the wrong one, and this is the line that shows it.
            System.err.println("jmdns browsing from ${dns.inetAddress} on ${java.net.NetworkInterface.getByInetAddress(dns.inetAddress)?.name}")
            dns.addServiceListener("$SERVICE_TYPE.local.", listener)
        }
    }

    override fun stop() {
        jmdns?.close()
        jmdns = null
    }
}
