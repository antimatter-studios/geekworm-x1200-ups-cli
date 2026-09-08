# geekworm-x1200-ups-cli

A small CLI that reports the battery and power state of a [Geekworm X1200](https://wiki.geekworm.com/X1200)
UPS HAT on a Raspberry Pi 5.

```
$ x1200
battery     95%   4.152 V   unknown
power       5.06 V   0.267 A   1.34 W
          shunt 10.000 mOhm, drop 3 mV, ina219
```

```
$ x1200 --json
{
  "battery": { "name": "battery", "percent": 95, "voltage_v": 4.1525, "status": "Unknown", "present": true },
  "power":   { "name": "hwmon6", "chip": "ina219", "bus_v": 5.06, "shunt_mv": 3,
               "current_a": 0.267, "watts_w": 1.34, "shunt_ohms": 0.01 }
}
```

## It reads sysfs, not I2C

This is the design decision everything else follows from. The two chips on the bus have in-tree
kernel drivers — `max17040_battery` for the fuel gauge and `ina2xx` for the power monitor — and once
those are bound they own their I2C addresses exclusively:

```
$ i2cget -y 1 0x36 0x02 w
Error: Could not set address to 0x36: Device or resource busy
```

Talking I2C directly would mean either forcing past that (`-f`, which can interleave with a driver
mid-transaction) or unbinding the drivers, which would cost `sensors`, `upower` and Netdata's
automatic charting of both devices. Reading what the drivers already decoded keeps all of it.

It also makes this program smaller and more durable than it would otherwise be. There is no I2C
address, register number or scaling constant anywhere in the source: the kernel did that arithmetic
and is better placed to. Devices are found by *shape* rather than by name — a hwmon publishing a bus
voltage, a current and a power is a power monitor whatever chip is underneath — so an INA226 or a
different HAT works without a code change.

## Setup on the Pi

I2C needs two things, and they fail separately, which is what makes it confusing. `dtparam=i2c_arm=on`
in `/boot/firmware/config.txt` brings up the header's controller; the `i2c-dev` module is what turns
it into `/dev/i2c-1`. With only the first, a reboot produces a working controller and no device file,
which looks exactly like the reboot not having happened.

The chips then have to be attached to their drivers. **I2C has no enumeration** — unlike USB or PCI,
a driver cannot ask the bus what is attached, because blind probing means writing to addresses
belonging to devices that may do something regrettable when poked. So something must assert that a
max17040 lives at 0x36 and an INA219 at 0x40:

```sh
echo max17040 0x36 | sudo tee /sys/bus/i2c/devices/i2c-1/new_device
echo ina219    0x40 | sudo tee /sys/bus/i2c/devices/i2c-1/new_device
```

Once asserted, the modules autoload from the modalias. A device tree overlay
(`dtoverlay=i2c-sensor,max17040`) is the more idiomatic Raspberry Pi way, but there is no overlay for
the INA219, so using it would describe one chip through the device tree and the other through sysfs —
two mechanisms, and a reboot needed to change one but not the other.

## Known limitations, and why they are not bugs

**`status` reads `Unknown`, permanently.** A fuel gauge measures charge; whether the pack is charging
or discharging is the charger's business, and there is no charger chip on the I2C bus to ask. The
X1200 signals mains presence on a GPIO instead, which is a different source. Reading it is the next
thing to build.

**There is no time-remaining.** The MAX17040 is a voltage-based gauge: it publishes a percentage and
a cell voltage and nothing else — no `charge_full`, no `current_now`. Time remaining needs the pack's
capacity in mAh, which is a fact about the cells fitted rather than something the hardware knows.

**The shunt resistance is probably wrong, and it scales everything.** The `ina2xx` driver defaults to
10 mΩ, and every current and power figure is directly proportional to it. On this board 10 mΩ is
ruled out by physics rather than by preference: it implies 1.35 W entering a board whose own PMIC
reports 2.73 W being delivered to rails downstream of it, and input cannot be less than what it
feeds. 5 mΩ puts idle draw at about 2.7 W, which matches, and is the sensible choice for a 5 A board —
0.1 Ω at 5 A would drop half a volt and burn 2.5 W. Until the board's documentation settles it, the
value is reported in the output so that nobody has to guess what the watts were divided by:

```sh
echo 5000 | sudo tee /sys/bus/i2c/devices/1-0040/hwmon/hwmon*/shunt_resistor
```

## Hardware notes

The X1200 carries two 18650 cells **in parallel** (1S2P), so pack voltage and cell voltage are the
same number and a full pack reads about 4.2 V. Geekworm's own documentation lists the MAX17040G+ at
0x36 but does not mention an INA219; the one at 0x40 is present and real on this board — its config
register reads `0x399f`, the INA219 power-on default — and it tracks the Pi's load rather than a
charge branch: driving four cores took it from 268 mA to 527 mA with the rail sagging from 5072 mV to
4952 mV.

## Development

```sh
make          # vet, test, build
make cover    # per-function coverage
make arm64    # cross-compile for the Pi; no toolchain needed, nothing here uses cgo
make deploy   # scp to a Pi and install to /usr/local/bin
```

The code is written functionally: every function is pure given its arguments, and the only impure
thing in the program is the pair of closures `sysfs.OS` returns. That is what makes it testable with
no hardware — `sysfs.Map` is a filesystem made of a map literal, so the discovery logic and all the
arithmetic run against fixtures on any machine.

Installing this properly is [Pulumi's](https://www.pulumi.com) job in the `homelab-server` stack.
`make deploy` exists for the loop before that is worth doing.
