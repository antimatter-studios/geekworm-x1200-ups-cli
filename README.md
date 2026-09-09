# geekworm-x1200-ups-cli

A small CLI that reports the battery and power state of a [Geekworm X1200](https://wiki.geekworm.com/X1200)
UPS HAT on a Raspberry Pi 5.

```
$ x1200
battery
  charge:    80%
  voltage:   4.023 V
  state:     on battery — mains lost

power
  bus:       5.04 V
  current:   0.526 A
  draw:      2.66 W
  shunt:     5.000 mOhm  (drop 3 mV, ina219)

supply
  source:    battery — MAINS LOST  (GPIO6)
  charging:  disabled  (GPIO16)

estimate
  state:     discharging
  remaining: 1h09m
  rate:      -68.6%/h
  note:      rate still settling; expect this to lengthen
```

Every value carries its own key. An earlier version put them in columns and left the reader to work
out which was which, which failed worst on the gauge's `status`: a bare `unknown` at the end of a
line gives no clue that it is describing charge direction.

```
$ x1200 --json
{
  "battery":  { "percent": 80, "voltage_v": 4.023, "status": "Unknown", "present": true },
  "power":    { "chip": "ina219", "bus_v": 5.04, "current_a": 0.526, "shunt_ohms": 0.005 },
  "supply":   { "on_mains": false, "gpio_line": 6 },
  "estimate": { "state": "discharging", "time_to_empty_s": 4140, "percent_per_hour": -68.6 }
}
```

## Time remaining, and why the obvious method is wrong

A single reading cannot answer "how long left" — the gauge reports a percentage and nothing about
time — so this keeps a short history of samples and measures how fast the number is moving. The file
lives in a tmpfs (`/tmp/x1200-history` by default, `--history` to move it, empty to disable). That is
deliberate on both counts: writes never reach the SD card, which matters on a Pi that has already
lost one card to write amplification, and the history is *meant* to vanish on reboot, because a rate
measured before a power cycle describes a different machine from the one after it.

Drawing a line between the first and last sample produces a confidently wrong answer, and it is
wrong in the worst direction for the first few minutes of every power cut — which is exactly when
somebody is reading it. Measured during a real mains failure:

```
21:22  94%      21:24  93%      21:25  92%      21:26  85%      21:29  80%
```

That is about 2%/min at the start and about 0.7%/min once settled. Most of the early drop is not
capacity leaving the pack; it is a voltage-based gauge re-converging after the load stepped up. A
naive fit across it reported **30 minutes** when the settled tail implied **over an hour**.

Four things defend against that:

- **A settling period is skipped** at the start of a run, because samples taken while the gauge is
  still converging describe the gauge rather than the battery.
- **The slope is a Theil-Sen estimator** — the median of all pairwise slopes — not a least-squares
  fit. A median ignores a minority of wild samples; a mean is dragged by them.
- **Pairs closer than a minute apart are discarded.** The percentage moves in whole steps, so two
  samples seconds apart differ by 0% or 1%, and their slope is either zero or enormous.
- **Deceleration is detected and reported.** The window is split in half and each half measured
  separately; if the recent half is materially shallower the gauge is still converging, so the recent
  figure is used and the output says `rate still settling; expect this to lengthen`.

On the capture above, that is the difference between reporting 30 minutes and reporting
`1h09m -68.6%/h (rate still settling; expect this to lengthen)` — where -68.6%/h is within a
percentage point of the last measured segment. The full capture is a test fixture in
`internal/estimate`, so any future change has to keep clearing it.

A run ends when the direction reverses, so charging is never averaged against discharging, and a
plateau does not end one — sitting at 80% for several samples while the voltage falls is normal on a
gauge that reports whole numbers.

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

## Mains or battery, and why it needs a GPIO

Nothing on the I2C bus can tell you whether the machine is running on mains — which is usually the
whole point of owning a UPS. The fuel gauge measures charge, not direction, so `status` reads
`Unknown` permanently and always will. The INA219 sits *downstream* of the changeover, so it
measures the same load either way. Neither chip can see the difference.

The board signals it on **GPIO6** instead, and the polarity is from Geekworm's hardware wiki:
"Low-power supply failed, High-power supply OK". High is mains.

That pin needs configuring before it can be read at all. An unconfigured line has no level —
`pinctrl get 6` shows `--` in the level column — so no amount of polling will catch an edge. Put
this in `/boot/firmware/config.txt` and reboot:

```
gpio=6=ip,pu
```

**GPIO16 is charging control, and it is active LOW.** The vendor disables charging with
`pinctrl set 16 op dh` and enables it with `op dl`. The related X728 is the other way round, so
anyone reasoning from that board — or from the intuition that high means on — disables charging while
believing they enabled it. Leaving the pin undriven lets the board charge normally, which is why
nothing here forces a level at boot.

### A high reading is weaker evidence than a low one

The line is read with a pull-up, so a floating pin reads high, and high means mains. A HAT that is
not seated, a pogo-pin contact gone intermittent, or the board removed entirely all report that
everything is fine. **The detector's failure mode is to say nothing is wrong.**

So a mains reading is cross-checked against the pack. A falling percentage is a measurement rather
than an absence, from a different chip on a different bus — and during an observed outage it was the
only source that told the truth. Where the two disagree, the output says so:

```
supply
  source:   mains  (GPIO6)
  SUSPECT:  GPIO6 reads mains but the pack is draining; a floating pin also
            reads mains, so check the HAT is seated
```

Deliberately not resolved in favour of either. Overriding the pin would be a guess about which
sensor is broken. Anything automatic built on this must treat a suspect reading as "assume the
worst".

## Known limitations, and why they are not bugs

**There is no time-remaining in the hardware.** The MAX17040 is a voltage-based gauge: it publishes a
percentage and a cell voltage and nothing else — no `charge_full`, no `current_now`. The estimate
above is derived from watching the percentage move over time, which is why it needs a few minutes of
history before it will say anything.

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
make            # vet, test, build
make cover      # per-function coverage
make arm64      # cross-compile for the Pi; no toolchain needed, nothing here uses cgo
make snapshot   # every release artefact, built but not published
make deploy     # scp to a Pi and install to /usr/local/bin
```

## Which build is this

```
$ x1200 version
x1200
  version:  v0.2.0
  source:   release
  commit:   2269a16724a91531f7255406ceb0c04f56cd031e
  built:    2026-09-09T07:08:39Z
  go:       go1.24.0
  platform: linux/arm64
```

`x1200 version -json` for the same thing machine-readably, and `x1200 -version` for one line.

A version string is only useful if it cannot lie, and the question is always the same: is the thing
running on that machine the thing I think I built? So the two kinds of build take their identity
from different places.

A **release** is built from a tag by the pipeline, which stamps the tag in. The tag is the identity,
because that is the name a person asks for.

A **local build** stamps nothing at all. Go embeds the commit and a dirty flag in the binary's own
build info, so it reports them without any help — and unlike a stamped string, that cannot be stale
or forgotten:

```
$ x1200 version
x1200
  version:  2269a16724a9-dirty
  source:   source (uncommitted changes)
```

The `-dirty` is in the version itself rather than only in the `source` field, because the version is
what gets pasted into an issue, and a build with uncommitted changes must not masquerade as a commit
somebody else could check out. `go run` is the one case that reports `unknown`: it does not embed VCS
information at all.

## Releases

Pushing a `v*` tag builds and publishes through [GoReleaser](https://goreleaser.com): tarballs and
Debian packages for linux amd64, arm64 and armv7, plus darwin amd64 and arm64, with a
`checksums.txt` beside them.

**Only tags produce binaries.** `release.yml` runs on `v*` tags and nothing else; `ci.yml` runs the
tests on every push and pull request and produces nothing installable. So every artefact that exists
anywhere corresponds to a tag somebody can name — there is no snapshot to be mistaken for a release,
and no way to install something that was never tagged. The tests still run continuously, because the
alternative is discovering a break at the moment of tagging, which is both the worst time to find it
and the point at which the pressure to ship anyway is highest.

The checksums are the point rather than a formality. Whatever installs this on the Pi should verify
what it downloaded instead of trusting the transfer, and that only works if every release is built
the same way with the same set of artefacts. `make snapshot` produces all of it locally, and CI does
the same on every push, so a packaging mistake surfaces before a tag rather than during one.

On Raspberry Pi OS the `.deb` is preferable to the tarball: `apt install ./x1200_*_arm64.deb` leaves
a record in dpkg, so anything auditing the machine later can see where the binary came from.

The code is written functionally: every function is pure given its arguments, and the only impure
thing in the program is the pair of closures `sysfs.OS` returns. That is what makes it testable with
no hardware — `sysfs.Map` is a filesystem made of a map literal, so the discovery logic and all the
arithmetic run against fixtures on any machine.

Installing this properly is [Pulumi's](https://www.pulumi.com) job in the `homelab-server` stack.
`make deploy` exists for the loop before that is worth doing.
