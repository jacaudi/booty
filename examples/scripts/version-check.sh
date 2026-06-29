#!/bin/bash

# Compare the locally-running Flatcar version against the version booty is
# currently serving; print a reboot notice when they differ.
set -a
. /etc/os-release
. <(curl http://${BOOTY_IP}/version.txt)
set +a

if [[ $ID == "flatcar" ]]; then
	echo "Local version:  $VERSION";
	echo "Remote version: $FLATCAR_VERSION";
	if [ "$FLATCAR_VERSION" != "$VERSION" ]; then
		echo "Need to reboot!";
	fi
fi
