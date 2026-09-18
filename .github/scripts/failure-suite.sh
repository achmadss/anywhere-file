#!/usr/bin/env bash
# #40's acceptance: every row of the failure table is a named test that ran and passed. A
# test that never ran reports the same green tick as one that did, so the names are checked
# here rather than trusted.
set -euo pipefail

go test -v -timeout 25m ./qa/ | tee suite.log

rows=(
	TestWithNoInternetTheLANWorksAndTheServerSaysTheDeviceIsOffline
	TestTheAgentComesBackWhenTheServerDoes
	TestARestartedAgentIsTheSamePC
	TestATunnelDroppedMidRequestFailsCleanly
	TestASecondTunnelReplacesTheFirst
	TestARevokedGuestIsDeniedAndTheLANIsUntouched
	TestAPCThatIsSwitchedOffIsReportedOffline
	TestNoFailureCostsTheDeviceKeyOrAFile
)
missing=0
for row in "${rows[@]}"; do
	if ! grep -q -- "--- PASS: $row" suite.log; then
		echo "$row did not run"
		missing=1
	fi
done
if [ "$missing" != 0 ]; then
	echo
	echo "The failure suite skipped rows of the table. A skip reports as a pass, so this is a failure."
	exit 1
fi
echo "every row of the table ran"
