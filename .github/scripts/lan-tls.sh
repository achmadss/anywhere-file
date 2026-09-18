#!/usr/bin/env bash
# Does to a running agent what a client does (#96): read the discovery document over TLS,
# check the device id is the digest of the public key in it, then check that key's
# signature over the certificate the connection is using. Written with openssl rather than
# with the agent's own code, so it is the wire that is checked and not Go talking to Go.
set -euo pipefail

addr="${RFM_AGENT_ADDR:-127.0.0.1:7433}"
work=$(mktemp -d)
cd "$work"

want=$("$AGENT" key | awk '/^device id:/ { print $3 }')
openssl s_client -connect "$addr" </dev/null 2>/dev/null >served.pem
curl -fsSk --max-time 5 "https://$addr/.well-known/anywhere-file" >doc.json
cat doc.json
echo

device=$(jq -r .device_id doc.json)
public=$(jq -r .public_key doc.json)
proof=$(jq -r .tls_proof doc.json)
if [ "$device" != "$want" ]; then
	echo "the document says device $device, this PC is $want"
	exit 1
fi

# The device id is the digest of the public key, which is what makes the id worth checking.
digest=$(printf '%s' "$public" | xxd -r -p | openssl dgst -sha256 -hex | awk '{ print $NF }')
if [ "$digest" != "$device" ]; then
	echo "the public key hashes to $digest, the document claims $device"
	exit 1
fi

# The message the device key signed: the context string, then the digest of the
# certificate's public key.
openssl x509 -in served.pem -pubkey -noout >cert-pub.pem
openssl pkey -pubin -in cert-pub.pem -outform DER -out cert-spki.der
printf 'anywhere-file lan-tls v1\n' >message
openssl dgst -sha256 -binary cert-spki.der >>message

# An Ed25519 public key as a PEM is the twelve byte header for the algorithm and the key.
{
	printf '302a300506032b6570032100'
	printf '%s' "$public"
} | xxd -r -p | openssl pkey -pubin -inform DER -pubout -out device-pub.pem
printf '%s' "$proof" | xxd -r -p >sig.bin

if ! openssl pkeyutl -verify -pubin -inkey device-pub.pem -rawin -in message -sigfile sig.bin; then
	echo "the device key did not sign the certificate the gateway is serving"
	openssl x509 -in served.pem -noout -text | head -20
	exit 1
fi
echo "device $device signed the certificate it is serving"
