#!/bin/sh
set -e

# Deliberately does NOT enable the timer.
#
# Installing a package should not start a machine writing to its own SD card every five minutes.
# That is a decision about what the machine does, and it belongs to whoever runs the machine — here
# it is declared in a Pulumi stack, and a package that enabled itself would put the machine out of
# step with its own description.
systemctl daemon-reload >/dev/null 2>&1 || true

cat <<'EOF'
x1200 installed. The sampler is not running yet:

  systemctl enable --now x1200.timer

Before that, check the prerequisites are in place:

  x1200 doctor

I2C needs `dtparam=i2c_arm=on` and the i2c-dev module; mains detection needs
`gpio=6=ip,pu` in /boot/firmware/config.txt and a reboot.
EOF
