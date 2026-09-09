#!/bin/sh
set -e

# Stop the timer before the binary goes, or systemd keeps firing a unit whose ExecStart has been
# removed and fills the journal with failures on a machine nobody is watching any more.
if [ -d /run/systemd/system ]; then
  systemctl stop x1200.timer >/dev/null 2>&1 || true
  systemctl disable x1200.timer >/dev/null 2>&1 || true
fi

# The archive under /var/lib is left in place on purpose. It is measurement history, it is the only
# record of how the pack has actually behaved, and removing a package is not a request to discard it.
