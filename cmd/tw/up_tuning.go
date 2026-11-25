package main

import (
	"flag"
	"fmt"
	"io"
	"time"
)

// The host's timing, which tw up no longer takes as flags (#457). They are
// written into tw.json when its field is empty, never left to the daemons'
// defaults: mgmt treats a device delete delay of 0 as an immediate delete,
// and `tw kill` prints the saved delay. A value an older tw saved is kept
// and used.
const (
	upHeartbeat         = time.Second
	upKillTombstone     = 12 * time.Second
	upDeviceDeleteDelay = 8 * time.Second // < upKillTombstone: the tombstone answers only while the device exists
)

// removedUpFlags were tw up flags until #457. They still parse (scripts and
// saved command lines keep working) but do nothing; tw up says so when one
// is given. --push-port was broken by construction: the policy grant
// allows only defaultPushPort.
var removedUpFlags = []string{
	"device-delete-delay",
	"heartbeat",
	"kill-tombstone",
	"push-port",
}

// addRemovedUpFlags registers removedUpFlags as accepted no-ops, hidden
// from --help.
func addRemovedUpFlags(f *flags) {
	f.hidden("deprecated and ignored", removedUpFlags...)
	f.Duration("heartbeat", 0, "deprecated, ignored: the host heartbeats every "+upHeartbeat.String())
	f.Duration("kill-tombstone", 0, "deprecated, ignored: a killed node answers object_killed for "+upKillTombstone.String())
	f.Duration("device-delete-delay", 0, "deprecated, ignored: a killed object's device is deleted "+upDeviceDeleteDelay.String()+" after the kill")
	f.Int("push-port", 0, fmt.Sprintf("deprecated, ignored: mgmt pushes kills to port %d (the port the tailnet policy grants)", defaultPushPort))
}

// warnRemovedUpFlags tells the user which removed flags were given.
func warnRemovedUpFlags(fs *flag.FlagSet, w io.Writer) {
	for _, n := range removedUpFlags {
		if flagGiven(fs, n) {
			fmt.Fprintf(w, "› note     --%s is deprecated and ignored; it will be removed in a later release\n", n)
		}
	}
}

// defaultTuning fills the host timing tw.json leaves empty.
func (c *Config) defaultTuning() {
	if c.Heartbeat == "" {
		c.Heartbeat = upHeartbeat.String()
	}
	if c.KillTombstone == "" {
		c.KillTombstone = upKillTombstone.String()
	}
	if c.DeviceDeleteDelay == "" {
		c.DeviceDeleteDelay = upDeviceDeleteDelay.String()
	}
}
