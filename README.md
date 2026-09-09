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
  "battery": {
    "name": "battery",
    "percent": 80,
    "voltage_v": 4.023,
    "status": "Unknown",
    "present": true
  },
  "power": {
    "name": "hwmon6",
    "chip": "ina219",
    "bus_v": 5.04,
    "shunt_mv": 3,
    "current_a": 0.526,
    "watts_w": 2.66,
    "shunt_ohms": 0.005
  },
  "estimate": {
    "state": "discharging",
    "percent_per_hour": -48.91323990545379,
    "time_to_empty_s": 5887.9763548,
    "samples": 10,
    "span_s": 327.97404,
    "note": "rate still settling; expect this to lengthen"
  }
}
```

On a machine where GPIO6 is readable a `supply` object appears alongside these, carrying `on_mains`,
`gpio_line` and — when the pin contradicts the gauge — `suspect`.


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

## Flags

| flag | default | what it does |
|---|---|---|
| `--json` | off | machine-readable output |
| `--watch` | `0` | repeat at an interval, e.g. `--watch 2s` |
| `--store` | `/var/lib/x1200/samples` | sample store; empty disables recording and everything derived from it |
| `--window` | `2h` | how much recent history the rate estimate looks at |
| `--max-gap` | `2m` | longest gap between samples that may be integrated across |
| `--capacity-mah` | `0` | declared pack capacity, for a runtime estimate; 0 omits it |
| `--trust-current` | off | report charge/energy totals; needs the INA219's circuit identified |
| `--gpio` | on | read mains and charging state from GPIO |
| `--chip` | auto | which gpiochip; empty picks the header controller |
| `--root` | `/sys` | sysfs root, for running against captured files |
| `--version` | | one-line identity; `x1200 version` for detail |

Subcommands:

| command | what it does |
|---|---|
| `version` | what this binary is, and whether it is a release or a local build |
| `doctor` | check the prerequisites, and name the fix for each that is missing |
| `calibrate` | fit the shunt resistance against the Pi's own power sensors |
| `record` | take one sample and append it to the store; what the timer runs |
| `systemd` | print the service and timer units |

`--root` is what makes this testable without hardware: point it at a directory of captured sysfs
files and the whole program runs on any machine.

## How it is put together

Eleven packages, each with one job, and the split follows a single rule: anything impure is injected
so that the logic can be tested against a map literal on a laptop with no Raspberry Pi attached.

| package | responsibility |
|---|---|
| `sysfs` | reads the kernel's small text files; the only real I/O is two closures in `OS()` |
| `ups` | turns what the drivers publish into one reading, and renders it |
| `estimate` | pure maths: a robust slope over samples, and a time remaining |
| `history` | persists samples so a rate can be measured between invocations |
| `gpio` | the Linux GPIO character device, by ioctl, with no external dependency |
| `x1200` | the pin numbers and polarities specific to this board |
| `coulomb` | integrates measured current into charge actually delivered |
| `calibrate` | fits the shunt resistance from a second, independent instrument |
| `pmic` | reads the Pi's own power sensors, the only vendor-tool dependency |
| `service` | the systemd units, and the single source of truth for them |
| `build` | what this binary is and where it came from |

`gpio` is the one place with `unsafe`, confined to a single `ioctl` function. Its struct layouts live
in `abi.go` with **no build tag** on purpose: they describe Linux but do not need Linux to compile,
so `go test` on any machine asserts the sizes, field offsets, ioctl request numbers and flag values
against the constants from `linux/gpio.h`. That matters because this project has no access to the Pi
it targets, and a struct one padding word out produces a bare `EINVAL` with nothing to say which
field was wrong.

`x1200` is where the genericity deliberately stops. Everything else finds devices by shape — a hwmon
publishing a bus voltage, a current and a power is a power monitor whatever chip is underneath — but
a pin number and a polarity are facts about one board and cannot be inferred from anything.

## Installing

Three ways in, and all three end up with identical unit files — see below for why that took effort.

**Debian package** (Raspberry Pi OS, and the one to prefer):

```sh
apt install ./x1200_0.2.0_linux_arm64.deb
```

Installs the binary to `/usr/bin/x1200` and the systemd units to `/lib/systemd/system`. It does
**not** enable the timer: installing a package should not start a machine writing to its own SD card
on a schedule nobody asked for. `dpkg` records what it placed, so anything auditing the machine
later can see where the files came from.

**Tarball**, which carries the units in a `systemd/` directory:

```sh
tar xzf x1200_0.2.0_linux_arm64.tar.gz
install -m 0755 x1200 /usr/local/bin/
cp systemd/x1200.* /etc/systemd/system/
```

**Binary alone**, downloaded on its own. Nothing is lost — the binary can emit the units itself:

```sh
x1200 systemd --binary /usr/local/bin/x1200 > /dev/null   # see them
x1200 systemd --only service > /etc/systemd/system/x1200.service
x1200 systemd --only timer   > /etc/systemd/system/x1200.timer
```

Then, however you installed:

```sh
systemctl daemon-reload
systemctl enable --now x1200.timer
```

Verify what you got with `x1200 version` — a release build reports `source: release` and its tag,
anything else reports the commit it was built from.

### Why the units live in Go

The program is no longer only a binary, and that creates a way to get quietly out of step. A package
can install a timer and a state directory; a downloaded binary installs neither. If the packaged
unit and the documented unit drift apart, the tool samples differently depending on how it was
installed — and that surfaces as missing data weeks later, with nothing to point at.

So the unit text lives in `internal/service`, `x1200 systemd` prints it, and `make units` writes the
copies that the `.deb` and the tarball ship. A test compares those copies against the code and fails
if they differ. Without that test it would just be two copies with extra steps.

Nothing in the tool installs or enables anything. Writing to `/etc` and running `systemctl` are
decisions about what the machine does, and on a machine described declaratively elsewhere, a tool
that reaches around that description is how a system stops matching what it says it is. The binary
produces text; something else decides what to do with it.

## The recording service

A single reading cannot say how long the pack will last, how much charge has actually moved, or what
the cells really hold. All three need the numbers watched over time, so a timer samples every five
minutes and appends to one file:

```
/var/lib/x1200/samples
```

`/var/lib` because that is where the Filesystem Hierarchy Standard puts state kept across reboots,
which is what this is — and it is worth more the longer it runs, since real capacity is only
measurable across a genuine discharge and those are rare.

One file, not two. There were briefly two — a short volatile window for the rate and a long
persistent archive — and the split did not survive the question "why". The estimator wants the
recent tail and the integrator wants all of it; that is two views of one file.

**It appends rather than rewriting, and that is not a detail.** Rewriting the whole store on every
sample costs its full size each time: at a few thousand samples that is tens of kilobytes, and 288
samples a day is around 17 MB daily, 6 GB a year of write amplification. This Pi has already
destroyed one SD card exactly that way. An append costs about 45 bytes — some 300× less — and the
full rewrite happens only when the file passes 1 MB, at which point it is pruned in one pass.

### The gap tolerance, which will bite you if you change the interval

The integrator refuses to integrate across a gap longer than `--max-gap`, because this tool also runs
on demand: one reading half an hour after another would otherwise contribute charge nobody measured.
The unit therefore passes `--max-gap` matching its own timer interval.

**Those two must agree.** A tolerance below the sampling interval refuses *every* interval, and the
result is not an error — it is a coverage of zero and no charge figures at all, silently. A test
asserts the unit and the timer stay in step, and the same reasoning ends a run in the rate estimator:
a gap longer than six minutes means the machine may have been off, so samples either side of it are
not read as one trend. That is what makes it safe for the store to outlive a reboot.

Coverage is always reported next to elapsed time for the same reason:

```
measured over: 20m of 20m elapsed
```

Equal values mean the sampler ran throughout. A covered time well below the span means the totals
describe only the minutes something was watching, not the period they appear to cover.

### What it gets you

```
delivered
  charge:           173.5 mAh
  energy:           0.88 Wh
  mean:             0.520 A
  measured over:    20m of 20m elapsed
  runtime:          11h32m at this rate  (assumes 6000 mAh declared, NOT measured)
  implied capacity: 2600 mAh, from the discharge trend at the measured current
                    —  the declared 6000 mAh is 2.3x higher
```

### The totals are withheld, and why

```
delivered
  ina219 mean:   0.520 A
  measured over: 20m of 20m elapsed
  WITHHELD:      charge and energy need the current's circuit identified; on this board
                 the INA219 does not track the Pi's load (525mA idle, 267mA at full
                 load), so integrating it would name a quantity nobody can.
                 pass --trust-current if you have established what it measures
```

Integrating a current only means something if you know which circuit it flows through, and **on this
board that is not established.** Measured against a load that doubled:

```
IDLE    curr=525 mA   pmic=2.72 W
LOAD    curr=267 mA   pmic=5.13 W     <- full load, lowest current
AFTER   curr=417 mA                   <- sustained, after the load was killed
```

It also sat flat across a mains transition — 0.264–0.271 A on battery, 0.266–0.301 A on mains — and
`hwmon` exposes no `update_interval`, so this is not a stale cache. Geekworm documents neither the
chip nor its shunt, and no schematic is published.

So the signal is real, stable and correctly read — and **unidentified**. Turning it into milliamp
hours and then into an implied pack capacity would produce confident figures from a current whose
circuit nobody can name, which is worse than reporting nothing, because it looks like a measurement.

The instrument's mean and the coverage are still shown: those are facts about the signal rather than
interpretations of it, and hiding them would conceal the evidence that anything is being measured at
all. The totals need `--trust-current`, which is a claim the operator makes and the tool cannot.

That is also why `x1200 calibrate` cannot presently succeed. Fitting the INA219 against the PMIC
assumes both instruments see the same current; they do not, so the fit is between unrelated signals
and correctly refuses. Two calibrations have now been rejected for two different reasons, which is
the best evidence available that the refusal logic is right.

`delivered` is kept apart from `battery` deliberately: the percentage is a *model* and this is a
*measurement*, and the gauge has been observed swinging eleven points in sixty seconds with no
charge movement behind it.

That last line is two independent measurements checking each other. The gauge says how fast the
percentage falls; the INA219 says how much current flows. If the pack held its declared capacity
those would agree. Where they diverge the declared figure is the suspect one — 18650 chemistry caps
around 3500 mAh per cell, so cells sold as 5000 mAh are overstated, and this says by how much
instead of leaving it a suspicion. A capacity is never inferred: pass `--capacity-mah` or the runtime
line is simply absent.

## Checking the setup: `x1200 doctor`

Every failure mode in this tool is silent by design, and that is the right behaviour taken one at a
time: an unreadable GPIO means the `supply` group is absent rather than wrong, an unbound driver
means the battery is absent rather than zero, and inventing a value in either case would be worse.
Taken together, though, it leaves no way to tell "this is fine" from "this has never worked".

```
$ x1200 doctor
[ ok ] i2c device        /dev/i2c-1 present, so i2cdetect works
[ ok ] fuel gauge        battery bound, reading 84%
[ ok ] power monitor     ina219 bound
[FAIL] shunt resistance  10.000 mOhm — the ina2xx default, provably wrong on this board
                  → every current, power, mAh and Wh figure is scaled by this, likely ~2x low.
                    run `x1200 calibrate` to fit it against the Pi's own sensors.
[FAIL] mains detection   GPIO6 not readable
                  → add `gpio=6=ip,pu` to /boot/firmware/config.txt and reboot. An unconfigured
                    line has no level at all — `pinctrl get 6` shows `--` — so no amount of
                    polling will catch an edge.
[ ok ] pi power sensors  5 rails, 2.73 W total

2 of 6 checks failed. The tool works, but some readings are missing or unscaled.
```

Every failing check names its remedy. A diagnostic that reports a problem without saying what to do
about it has moved the work rather than done it.

### One check worth knowing about: unit shadowing

The `.deb` installs its units to `/lib/systemd/system`, because that is where packaged units belong.
A machine's own configuration goes in `/etc/systemd/system` — **and systemd prefers `/etc`.**

So a unit written into `/etc` does not race the packaged one, it overrides it permanently and
silently. dpkg goes on owning its copy, package upgrades go on replacing it, and none of that has
any effect. Ship a corrected unit in a new release and the machine keeps running the stale override
with nothing anywhere to say why. That is worse than two things racing for one path, which at least
announces itself by being non-deterministic.

`doctor` reports it when both copies exist, and names the remedy:

```
[FAIL] unit shadowing   x1200.timer exists in BOTH /etc and /lib; the /etc copy wins
                        and the packaged one is inert
                  → Either delete the /etc copy and let the package own the unit, or
                    replace it with a drop-in that composes instead of hiding:
                      /etc/systemd/system/x1200.timer.d/override.conf
```

A drop-in is the right answer if the change was wanted: it composes with the packaged unit, so the
override survives upgrades and the upgrade survives the override. Configuration management should own
*whether the timer runs*, not the file's contents.

Failures are ranked, not merely listed, and there are three severities because they make three
different claims:

| | meaning |
|---|---|
| `STOP` | the tool cannot report anything useful; exits non-zero |
| `FAIL ` | you have configured something wrongly, and here is the correction; exits zero |
| `known` | a limitation of the hardware that no configuration will change |

The third exists so that `FAIL` keeps its meaning. The shunt being uncalibrated is a repair somebody
can make; the INA219's circuit being undocumented is not. Putting both under one label dilutes the
checks that can be acted on, and a reader who cannot tell them apart learns to skim both. A `known`
entry still carries an instruction — it just is not a repair:

```
[known] current source    the INA219 does not track the Pi's load, so its circuit is unidentified
                          → charge, energy and implied capacity are withheld because of this —
                            that is deliberate, not a fault. Do not pass --trust-current until
                            you have established what the current measures.
```

Known limitations are counted separately in the summary, so "nothing is misconfigured" stays sayable
on a machine that still has an undocumented sensor on it. `/dev/i2c-1` is deliberately
*not* required — this tool reads sysfs, so it needs the drivers bound; that device node is what
`i2cdetect` uses. A check that called a working system broken would teach you to ignore the report.

## Calibrating the shunt: `x1200 calibrate`

The shunt resistance is the one constant that silently scales everything. Current, power, mAh and Wh
are all a voltage drop divided by it, so a wrong value is wrong everywhere by the same factor and
**nothing in the output contradicts it**. The `ina2xx` driver defaults to 10 mΩ, and here that is not
merely unverified but provably wrong: it implies 1.34 W entering a board whose own PMIC reports
2.73 W delivered to rails downstream of it, and input cannot be less than what it feeds.

The Pi's PMIC is the way out. It reports per-rail voltage and current through firmware, sharing no
chip, bus or driver with the INA219 — which is the only reason comparing them is worth anything. Two
readings from the same sensor agreeing proves nothing.

```
$ x1200 calibrate
Sampling the INA219 against the Pi's PMIC, 12 times 2s apart.
Vary the load while this runs — `yes > /dev/null` on a few cores, then stop them.
A calibration taken entirely at idle constrains the answer barely at all.

   1/12  drop  2.67 mV   rail 5.087 V   pmic 2.730 W
   ...

fitted shunt resistance
  from slope:    5.1240 mOhm   <- prefer this; a fixed offset cannot bias it
  from mean:     5.3020 mOhm
  nearest part:  5.0000 mOhm   (2.5% away)
  points:        12 over 0.893 A of load range
  spread:        6.2%
```

Four things about that output are deliberate.

**The slope is the headline, not the average.** Fitting how the drop *grows* with current makes the
answer immune to any fixed offset — the HAT's own quiescent draw, for instance — which an average
absorbs into the result. Both are printed because their *disagreement* is itself the evidence that
such an offset exists.

### Never fit to `in0_input`

The first real calibration on hardware refused to produce a figure, and it was right to — but for an
artefact rather than a genuine disagreement. hwmon publishes `in0_input`, the shunt drop, in **whole
millivolts**, and on this board the entire signal is only a few millivolts wide. Measured against a
load that doubled:

```
in0=3 mV   curr1=266 mA   -> real drop 2.66 mV
in0=4 mV   curr1=395 mA   -> real drop 3.95 mV
in0=3 mV   curr1=267 mA   -> real drop 2.67 mV
```

The rounding step is around 37% of the signal. A slope fitted to that is fitted to rounding error;
30 samples across 0.548 A of load gave a spread of 110% and an implausible result.

The current register carries the same measurement at 10 µV per LSB — a hundredfold better — because
the driver computes it before anything is rounded. Multiplying it back by the resistance the driver
divided by recovers the true drop:

```
drop = curr1_input × shunt_resistor
```

**And the driver's assumed resistance cancels exactly.** It computed current as `drop ÷ assumed`, so
multiplying by `assumed` returns `drop` whatever the assumption was — which is what makes this usable
for calibration, where that assumption is precisely the unknown. Verified against the raw register
read by hand before the drivers claimed the address: 266 mA × 0.01 Ω = 2.66 mV.

`in0_input` is still displayed, because it is what the kernel publishes. Nothing quantitative is
built on it.

**The spread is the confidence signal.** Two instruments that agree at every load level produce a
tight spread; one that disagrees produces a wide one. Above 25% the tool says to treat the figure as
indicative only, and if the drop does not grow with load at all it refuses to offer a value, because
that is not a resistor.

**A single point is not a calibration.** Fewer than three is rejected outright: two sensors can agree
once by accident. So is a run taken entirely at idle, however many samples it contains — the load
range is printed so you can see whether you actually varied it.

**The bias is stated, because it cannot be removed.** The PMIC measures power *downstream* of the
shunt, so true input power is higher by the converter's efficiency, and this fit is therefore biased
high by roughly that factor — perhaps 5–10%. Nothing here can measure efficiency, so the honest move
is to name the error rather than quietly carry it.

### It prints; it does not apply

```
Nothing has been changed. To use it, either declare it where this machine is
described, or set it directly:

  echo 5000 | sudo tee /sys/bus/i2c/devices/1-0040/hwmon/hwmon*/shunt_resistor
```

Writing that value into sysfs is a state change on a machine that may be described declaratively
elsewhere, and a tool that reconfigures the machine behind that description is how a system stops
matching what it says it is. It also does not survive a reboot on its own, which is the second reason
it belongs in whatever describes the machine rather than in a shell.

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
  go:       go1.24.0   # whatever built it
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
