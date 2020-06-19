package core

var (
	status   = map[bool]string{false: "disable", true: "enable"}
	presence = map[bool]string{false: "—", true: "✓"}

	Voltage float64 // Voltage global
)

// powerToCurrent is a helper function to convert power to per-phase current
func powerToCurrent(power, voltage float64, phases int64) int64 {
	return int64(power / (float64(phases) * voltage))
}

// consumedPower estimates how much power the charger might have consumed given it was the only load
// func consumedPower(pv, battery, grid float64) float64 {
// 	return math.Abs(pv) + battery + grid
// }

// sitePower returns the available delta power that the charger might additionally consume
// negative value: available power (grid export), positive value: grid import
func sitePower(grid, battery, residual float64) float64 {
	return grid + battery + residual
}
