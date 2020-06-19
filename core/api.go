package core

import (
	"time"

	"github.com/andig/evcc/core/wrapper"
)

func (lp *LoadPoint) hasChargeMeter() bool {
	_, isWrapped := lp.chargeMeter.(*wrapper.ChargeMeter)
	return lp.chargeMeter != nil && !isWrapped
}

// chargeDuration returns for how long the charge cycle has been running
func (lp *LoadPoint) chargeDuration() time.Duration {
	d, err := lp.chargeTimer.ChargingTime()
	if err != nil {
		lp.log.ERROR.Printf("charge timer error: %v", err)
	}
	return d
}

// chargedEnergy returns energy consumption since charge start in kWh
func (lp *LoadPoint) chargedEnergy() float64 {
	f, err := lp.chargeRater.ChargedEnergy()
	if err != nil {
		lp.log.ERROR.Printf("charge rater error: %v", err)
	}
	return f
}

// remainingChargeDuration returns the remaining charge time
func (lp *LoadPoint) remainingChargeDuration(chargePercent float64) time.Duration {
	if !lp.charging {
		return -1
	}

	if lp.chargePower > 0 && lp.vehicle != nil {
		whRemaining := (1 - chargePercent/100.0) * 1e3 * float64(lp.vehicle.Capacity())
		return time.Duration(float64(time.Hour) * whRemaining / lp.chargePower)
	}

	return -1
}

// publish state of charge and remaining charge duration
func (lp *LoadPoint) publishSoC() {
	if lp.vehicle == nil {
		return
	}

	if lp.connected() {
		f, err := lp.vehicle.ChargeState()
		if err == nil {
			lp.log.DEBUG.Printf("vehicle soc: %.1f%%", f)
			lp.publish("socCharge", f)
			lp.publish("chargeEstimate", lp.remainingChargeDuration(f))
			return
		}
		lp.log.ERROR.Printf("vehicle error: %v", err)
	}

	lp.publish("socCharge", -1)
	lp.publish("chargeEstimate", -1)
}
