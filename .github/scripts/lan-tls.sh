#!/usr/bin/env bash
# The gateway has to answer with a certificate holding the device key (#96). That key is
# what a client pins, so a certificate signed by anything else is a pin on a key nobody
# can check against the device id.
set -euo pipefail

addr="${RFM_AGENT_ADDR:-127.0.0.1:7433}"
want=$("$AGENT" key | awk '/^public key:/ { print $3 }')

openssl s_client -connect "$addr" </dev/null 2>/dev/null >served.pem
# An Ed25519 public key is the last 32 bytes of the DER the certificate carries.
got=$(openssl x509 -in served.pem -pubkey -noout |
	openssl pkey -pubin -outform DER |
	tail -c 32 | od -An -tx1 | tr -d ' \n')

if [ -z "$want" ] || [ "$got" != "$want" ]; then
	echo "the gateway served $got, this PC's device key is $want"
	openssl x509 -in served.pem -noout -text | head -20
	exit 1
fi
echo "the gateway answers with the device key $want"
