//go:build !windows && !darwin

package main

// lanBlocked has nothing cheap and reliable to ask on Linux. A machine can run firewalld,
// ufw or plain nftables, and there is no one place that says what they let in, so the
// install prints which ports to open instead (packaging/linux/install.sh).
func lanBlocked() lanProblem { return lanFine }
