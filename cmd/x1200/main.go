// Command x1200 reports the state of a Geekworm X1200 UPS HAT.
//
// It reads only sysfs, so it needs no privileges and does not contend with the kernel drivers that
// own the I2C addresses. See internal/ups for why that matters.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/sysfs"
	"github.com/antimatter-studios/geekworm-x1200-ups-cli/internal/ups"
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

// options is what the flags parsed to, kept separate from the parsing so that run can be exercised
// without a process, a clock or a real filesystem.
type options struct {
	root  string
	json  bool
	watch time.Duration
}

// main is wiring and nothing else: every decision it makes is in parse or run, both of which are
// tested. What is left here is the impure edges — the real filesystem, the real output stream, the
// real clock, and the exit status.
func main() {
	opts, done, err := parse(os.Args[1:], os.Stderr)
	if err != nil {
		os.Exit(2)
	}
	if done {
		return
	}
	if err := run(sysfs.OS(opts.root), os.Stdout, time.Sleep, opts); err != nil {
		fmt.Fprintln(os.Stderr, "x1200:", err)
		os.Exit(1)
	}
}

// parse turns arguments into options.
//
// done reports that the work is finished and nothing should be read — currently only -version,
// which has to print before any attempt to find hardware, so that a binary on the wrong machine can
// still say what it is.
func parse(args []string, errOut io.Writer) (opts options, done bool, err error) {
	fs := flag.NewFlagSet("x1200", flag.ContinueOnError)
	fs.SetOutput(errOut)
	showVersion := fs.Bool("version", false, "print version and exit")
	fs.StringVar(&opts.root, "root", "/sys", "sysfs root, for testing against captured files")
	fs.BoolVar(&opts.json, "json", false, "emit JSON instead of text")
	fs.DurationVar(&opts.watch, "watch", 0, "repeat at this interval, e.g. 2s; 0 reads once")

	if err := fs.Parse(args); err != nil {
		return opts, false, err
	}
	if *showVersion {
		fmt.Fprintln(errOut, version)
		return opts, true, nil
	}
	return opts, false, nil
}

// run reads and renders, repeatedly if asked.
//
// sleep is a parameter rather than a call to time.Sleep so that a test of the watch loop takes no
// time and needs no goroutine.
func run(fs sysfs.FS, out io.Writer, sleep func(time.Duration), opts options) error {
	for {
		text, err := once(fs, opts.json)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(out, text); err != nil {
			return err
		}
		if opts.watch <= 0 {
			return nil
		}
		sleep(opts.watch)
	}
}

// once produces exactly one rendering. Pure given fs.
func once(fs sysfs.FS, asJSON bool) (string, error) {
	reading, err := ups.Read(fs)
	if err != nil {
		return "", err
	}
	if asJSON {
		return ups.JSON(reading)
	}
	return ups.Text(reading), nil
}
