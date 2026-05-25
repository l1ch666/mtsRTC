package runtime

// MultipathConfig controls stream-level striping across multiple independent
// carrier transports. The first ControlLanes are kept out of normal SOCKS
// traffic so liveness/control streams are not starved by browser bursts.
type MultipathConfig struct {
	Lanes              int
	ControlLanes       int
	ConnectParallelism int
	MinReady           int
	MaxStreamsPerLane  int
}

const (
	DefaultMultipathControlLanes       = 1
	DefaultMultipathConnectParallelism = 2
	DefaultMultipathMinReady           = 4
	DefaultMultipathMaxStreamsPerLane  = 3
	MaxMultipathLanes                  = 24
)

// Enabled reports whether a multipath pool should be used.
func (c MultipathConfig) Enabled() bool { return c.Lanes > 1 }

// WithDefaults returns a bounded, internally consistent config.
func (c MultipathConfig) WithDefaults() MultipathConfig {
	if c.Lanes < 0 {
		c.Lanes = 0
	}
	if c.Lanes > MaxMultipathLanes {
		c.Lanes = MaxMultipathLanes
	}
	if c.Lanes <= 1 {
		return MultipathConfig{}
	}
	if c.ControlLanes <= 0 {
		c.ControlLanes = DefaultMultipathControlLanes
	}
	if c.ControlLanes >= c.Lanes {
		c.ControlLanes = c.Lanes - 1
	}
	if c.ConnectParallelism <= 0 {
		c.ConnectParallelism = DefaultMultipathConnectParallelism
	}
	if c.ConnectParallelism > c.Lanes {
		c.ConnectParallelism = c.Lanes
	}
	if c.MinReady <= 0 {
		c.MinReady = DefaultMultipathMinReady
	}
	if c.MinReady > c.Lanes {
		c.MinReady = c.Lanes
	}
	if c.MaxStreamsPerLane <= 0 {
		c.MaxStreamsPerLane = DefaultMultipathMaxStreamsPerLane
	}
	return c
}
