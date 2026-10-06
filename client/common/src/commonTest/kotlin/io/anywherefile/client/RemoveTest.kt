package io.anywherefile.client

import kotlin.test.Test
import kotlin.test.assertEquals

// The dialog an admin reads before removing a PC for everyone (#189).
class RemoveTest {
    @Test
    fun itNamesTheDeviceAndCountsTheGuests() {
        assertEquals(
            "Everyone who can reach pc1 loses access, including your 2 guests. If pc1 comes back online, " +
                "it signs out. To add it again, sign in on its settings page.",
            removeForEveryone("pc1", 2),
        )
    }

    @Test
    fun oneGuestIsNotPlural() {
        assertEquals(
            "Everyone who can reach pc1 loses access, including your guest. If pc1 comes back online, " +
                "it signs out. To add it again, sign in on its settings page.",
            removeForEveryone("pc1", 1),
        )
    }

    @Test
    fun noGuestsLeavesThemOut() {
        assertEquals(
            "Everyone who can reach pc1 loses access. If pc1 comes back online, it signs out. " +
                "To add it again, sign in on its settings page.",
            removeForEveryone("pc1", 0),
        )
    }
}
