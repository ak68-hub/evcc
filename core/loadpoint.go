package core

import (
	"time"

	"github.com/andig/evcc/api"
	"github.com/andig/evcc/core/wrapper"
	"github.com/andig/evcc/push"
	"github.com/andig/evcc/util"
	"github.com/pkg/errors"

	evbus "github.com/asaskevich/EventBus"
	"github.com/avast/retry-go"
	"github.com/benbjohnson/clock"
)

const (
	evStartCharge   = "start"   // update chargeTimer
	evStopCharge    = "stop"    // update chargeTimer
	evChargeCurrent = "current" // update fakeChargeMeter
	evChargePower   = "power"   // update chargeRater
)

// ThresholdConfig defines enable/disable hysteresis parameters
type ThresholdConfig struct {
	Delay     time.Duration
	Threshold float64
}

//XXgo:generate mockgen -package mock -destination ../mock/mock_chargerhandler.go github.com/andig/evcc/core ChargerHandler

// LoadPoint is responsible for controlling charge depending on
// SoC needs and power availability.
type LoadPoint struct {
	ID int

	clock    clock.Clock       // mockable time
	bus      evbus.Bus         // event bus
	pushChan chan<- push.Event // notifications
	uiChan   chan<- util.Param // client push messages

	// exposed public configuration
	Title           string `mapstructure:"title"`   // UI title
	Phases          int64  `mapstructure:"phases"`  // Phases- required for converting power and current
	ChargerRef      string `mapstructure:"charger"` // Charger reference
	VehicleRef      string `mapstructure:"vehicle"` // Vehicle reference
	ChargeMeterRef  string `mapstructure:"charge"`  // Charge meter reference
	Enable, Disable ThresholdConfig

	// Site *Site

	ChargerHandler `mapstructure:",squash"` // handle charger state and current

	chargeTimer api.ChargeTimer
	chargeRater api.ChargeRater

	chargeMeter api.Meter   // Charger usage meter
	vehicle     api.Vehicle // Vehicle

	// cached state
	status      api.ChargeStatus // Charger status
	charging    bool             // Charging cycle
	chargePower float64          // Charging power
	sitePower   float64          // Available power from site

	pvTimer time.Time
}

// NewLoadPointFromConfig creates a new loadpoint
func NewLoadPointFromConfig(log *util.Logger, cp configProvider, other map[string]interface{}) *LoadPoint {
	lp := NewLoadPoint()
	util.DecodeOther(log, other, &lp)

	if lp.ChargerRef != "" {
		lp.charger = cp.Charger(lp.ChargerRef)
	} else {
		log.FATAL.Fatal("config: missing charger")
	}
	if lp.ChargeMeterRef != "" {
		lp.chargeMeter = cp.Meter(lp.ChargeMeterRef)
	}
	if lp.VehicleRef != "" {
		lp.vehicle = cp.Vehicle(lp.VehicleRef)
	}

	return lp
}

// NewLoadPoint creates a LoadPoint with sane defaults
func NewLoadPoint() *LoadPoint {
	clock := clock.New()
	bus := evbus.New()

	lp := &LoadPoint{
		clock:          clock, // mockable time
		bus:            bus,   // event bus
		Phases:         1,
		status:         api.StatusNone,
		ChargerHandler: NewChargerHandler("main", clock, bus),
	}

	return lp
}

// notify sends push messages to clients
func (lp *LoadPoint) notify(event string, attributes map[string]interface{}) {
	attributes["loadpoint"] = lp.Name
	lp.pushChan <- push.Event{
		Event:      event,
		Attributes: attributes,
	}
}

// publish sends values to UI and databases
func (lp *LoadPoint) publish(key string, val interface{}) {
	lp.uiChan <- util.Param{
		LoadPoint: lp.Name,
		Key:       key,
		Val:       val,
	}
}

// evChargeStartHandler sends external start event
func (lp *LoadPoint) evChargeStartHandler() {
	lp.notify(evStartCharge, map[string]interface{}{
		// "mode": lp.GetMode(),
	})
}

// evChargeStartHandler sends external stop event
func (lp *LoadPoint) evChargeStopHandler() {
	energy, err := lp.chargeRater.ChargedEnergy()
	if err != nil {
		log.ERROR.Printf("%s charged energy: %v", lp.Name, err)
	}

	duration, err := lp.chargeTimer.ChargingTime()
	if err != nil {
		log.ERROR.Printf("%s charge duration: %v", lp.Name, err)
	}

	lp.notify(evStopCharge, map[string]interface{}{
		"energy":   energy,
		"duration": duration.Truncate(time.Second),
	})
}

// evChargeCurrentHandler updates the dummy charge meter's charge power. This simplifies the main flow
// where the charge meter can always be treated as present. It assumes that the charge meter cannot consume
// more than total household consumption. If physical charge meter is present this handler is not used.
func (lp *LoadPoint) evChargeCurrentHandler(current int64) {
	power := float64(current*lp.Phases) * Voltage

	if !lp.enabled || lp.status != api.StatusC {
		// if disabled we cannot be charging
		power = 0
	}
	// TODO
	// else if power > 0 && lp.Site.pvMeter != nil {
	// 	// limit charge power to generation plus grid consumption/ minus grid delivery
	// 	// as the charger cannot have consumed more than that
	// 	// consumedPower := consumedPower(lp.pvPower, lp.batteryPower, lp.gridPower)
	// 	consumedPower := lp.Site.consumedPower()
	// 	power = math.Min(power, consumedPower)
	// }

	// handler only called if charge meter was replaced by dummy
	lp.chargeMeter.(*wrapper.ChargeMeter).SetPower(power)

	// expose for UI
	lp.publish("chargeCurrent", current)
}

// Prepare loadpoint configuration by adding missing helper elements
func (lp *LoadPoint) Prepare(uiChan chan<- util.Param, pushChan chan<- push.Event) {
	lp.pushChan = pushChan
	lp.uiChan = uiChan

	// ensure charge meter exists
	if lp.chargeMeter == nil {
		if mt, ok := lp.charger.(api.Meter); ok {
			lp.chargeMeter = mt
		} else {
			mt := &wrapper.ChargeMeter{}
			_ = lp.bus.Subscribe(evChargeCurrent, lp.evChargeCurrentHandler)
			_ = lp.bus.Subscribe(evStopCharge, func() {
				mt.SetPower(0)
			})
			lp.chargeMeter = mt
		}
	}

	// ensure charge rater exists
	if rt, ok := lp.charger.(api.ChargeRater); ok {
		lp.chargeRater = rt
	} else {
		rt := wrapper.NewChargeRater(lp.Name, lp.chargeMeter)
		_ = lp.bus.Subscribe(evChargePower, rt.SetChargePower)
		_ = lp.bus.Subscribe(evStartCharge, rt.StartCharge)
		_ = lp.bus.Subscribe(evStopCharge, rt.StopCharge)
		lp.chargeRater = rt
	}

	// ensure charge timer exists
	if ct, ok := lp.charger.(api.ChargeTimer); ok {
		lp.chargeTimer = ct
	} else {
		ct := wrapper.NewChargeTimer()
		_ = lp.bus.Subscribe(evStartCharge, ct.StartCharge)
		_ = lp.bus.Subscribe(evStopCharge, ct.StopCharge)
		lp.chargeTimer = ct
	}

	// event handlers
	_ = lp.bus.Subscribe(evStartCharge, lp.evChargeStartHandler)
	_ = lp.bus.Subscribe(evStopCharge, lp.evChargeStopHandler)

	// read initial enabled state
	enabled, err := lp.charger.Enabled()
	if err == nil {
		lp.enabled = enabled
		log.INFO.Printf("%s charger %sd", lp.Name, status[lp.enabled])

		// prevent immediately disabling charger
		if lp.enabled {
			lp.guardUpdated = lp.clock.Now()
		}
	} else {
		log.ERROR.Printf("%s charger error: %v", lp.Name, err)
	}

	// set current to known value
	if err = lp.setTargetCurrent(lp.MinCurrent); err != nil {
		log.ERROR.Println(err)
	}
	lp.bus.Publish(evChargeCurrent, lp.MinCurrent)
}

// connected returns the EVs connection state
func (lp *LoadPoint) connected() bool {
	return lp.status == api.StatusB || lp.status == api.StatusC
}

// chargingCycle detects charge cycle start and stop events and manages the
// charge energy counter and charge timer. It guards against duplicate invocation.
func (lp *LoadPoint) chargingCycle(start bool) {
	if start == lp.charging {
		return
	}

	lp.charging = start

	if start {
		log.INFO.Printf("%s start charging ->", lp.Name)
		lp.bus.Publish(evStartCharge)
	} else {
		log.INFO.Printf("%s stop charging <-", lp.Name)
		lp.bus.Publish(evStopCharge)
	}
}

// updateChargeStatus updates car status and detects car connected/disconnected events
func (lp *LoadPoint) updateChargeStatus() error {
	status, err := lp.charger.Status()
	if err != nil {
		return err
	}

	log.DEBUG.Printf("%s charger status: %s", lp.Name, status)

	if prevStatus := lp.status; status != prevStatus {
		lp.status = status

		// connected
		if prevStatus == api.StatusA {
			log.INFO.Printf("%s car connected (%s)", lp.Name, string(status))
			if lp.enabled {
				// when car connected don't disable right away
				lp.guardUpdated = lp.clock.Now()
			}
		}

		// disconnected
		if status == api.StatusA {
			log.INFO.Printf("%s car disconnected", lp.Name)
		}

		lp.bus.Publish(evChargeCurrent, lp.targetCurrent)

		// start/stop charging cycle
		lp.chargingCycle(status == api.StatusC)
	}

	return nil
}

func (lp *LoadPoint) maxCurrent(mode api.ChargeMode) int64 {
	// grid meter will always be available, if as wrapped pv meter
	targetPower := lp.chargePower - lp.sitePower
	log.DEBUG.Printf("%s target power: %.0fW = %.0fW charge - %.0fW available", lp.Name, targetPower, lp.chargePower, lp.sitePower)

	// get max charge current
	targetCurrent := clamp(powerToCurrent(targetPower, Voltage, lp.Phases), 0, lp.MaxCurrent)

	if mode == api.ModeMinPV && targetCurrent < lp.MinCurrent {
		return lp.MinCurrent
	}

	if mode == api.ModePV && lp.enabled && targetCurrent < lp.MinCurrent {
		// kick off disable sequence
		if lp.sitePower >= lp.Disable.Threshold {
			log.DEBUG.Printf("%s site power %.0f >= disable threshold %.0f", lp.Name, lp.sitePower, lp.Disable.Threshold)

			if lp.pvTimer.IsZero() {
				log.DEBUG.Printf("%s start disable timer", lp.Name)
				lp.pvTimer = lp.clock.Now()
			}

			if lp.clock.Since(lp.pvTimer) >= lp.Disable.Delay {
				log.DEBUG.Printf("%s disable timer elapsed", lp.Name)
				return 0
			}
		} else {
			// reset timer
			lp.pvTimer = lp.clock.Now()
		}

		return lp.MinCurrent
	}

	if mode == api.ModePV && !lp.enabled {
		// kick off enable sequence
		if targetCurrent >= lp.MinCurrent ||
			(lp.Enable.Threshold != 0 && lp.sitePower <= lp.Enable.Threshold) {
			log.DEBUG.Printf("%s site power %.0f < enable threshold %.0f", lp.Name, lp.sitePower, lp.Enable.Threshold)

			if lp.pvTimer.IsZero() {
				log.DEBUG.Printf("%s start enable timer", lp.Name)
				lp.pvTimer = lp.clock.Now()
			}

			if lp.clock.Since(lp.pvTimer) >= lp.Enable.Delay {
				log.DEBUG.Printf("%s enable timer elapsed", lp.Name)
				return lp.MinCurrent
			}
		} else {
			// reset timer
			lp.pvTimer = lp.clock.Now()
		}

		return 0
	}

	log.DEBUG.Printf("%s timer reset", lp.Name)

	// reset pv timer
	lp.pvTimer = time.Time{}

	return targetCurrent
}

// updateModePV handles "minpv" or "pv" modes by setting charger enabled/disabled state
// and maximum current according to available PV power
func (lp *LoadPoint) updateModePV(mode api.ChargeMode) error {
	targetCurrent := lp.maxCurrent(mode)
	if !lp.connected() {
		// ensure minimum current when not connected
		// https://github.com/andig/evcc/issues/105
		targetCurrent = min(lp.MinCurrent, targetCurrent)
	}

	log.DEBUG.Printf("%s target charge current: %dA", lp.Name, targetCurrent)

	if targetCurrent == 0 {
		return lp.rampOff()
	}

	if !lp.enabled {
		return lp.rampOn(targetCurrent)
	}

	return lp.rampUpDown(targetCurrent)
}

// updateMeter updates and publishes single meter
func (lp *LoadPoint) updateMeter(name string, meter api.Meter, power *float64) error {
	value, err := meter.CurrentPower()
	if err != nil {
		return err
	}

	*power = value // update value if no error

	log.DEBUG.Printf("%s %s power: %.1fW", lp.Name, name, *power)
	lp.publish(name+"Power", *power)

	return nil
}

// updateMeter updates and publishes single meter
func (lp *LoadPoint) updateMeters() (err error) {
	retryMeter := func(s string, m api.Meter, f *float64) {
		if m != nil {
			e := retry.Do(func() error {
				return lp.updateMeter(s, m, f)
			}, retry.Attempts(3))

			if e != nil {
				err = errors.Wrapf(e, "updating %s meter", s)
				log.ERROR.Printf("%s %v", lp.Name, err)
			}
		}
	}

	// read PV meter before charge meter
	retryMeter("charge", lp.chargeMeter, &lp.chargePower)

	return err
}

// syncSettings synchronizes charger settings to expected state
func (lp *LoadPoint) syncSettings() {
	enabled, err := lp.charger.Enabled()
	if err == nil && enabled != lp.enabled {
		log.DEBUG.Printf("%s sync enabled state to %s", lp.Name, status[lp.enabled])
		err = lp.charger.Enable(lp.enabled)
	}

	if err != nil {
		log.ERROR.Printf("%s charge controller error: %v", lp.Name, err)
	}
}

// update is the main control function. It reevaluates meters and charger state
func (lp *LoadPoint) Update(mode api.ChargeMode, sitePower float64) float64 {
	// read and publish meters first
	meterErr := lp.updateMeters()

	lp.sitePower = sitePower

	// update ChargeRater here to make sure initial meter update is caught
	lp.bus.Publish(evChargeCurrent, lp.targetCurrent)
	lp.bus.Publish(evChargePower, lp.chargePower)

	// read and publish status
	if err := retry.Do(lp.updateChargeStatus, retry.Attempts(3)); err != nil {
		log.ERROR.Printf("%s charge controller error: %v", lp.Name, err)
		return lp.chargePower
	}

	lp.publish("connected", lp.connected())
	lp.publish("charging", lp.charging)

	// sync settings with charger
	if lp.status != api.StatusA {
		lp.syncSettings()
	}

	// check if car connected and ready for charging
	var err error

	// execute loading strategy
	switch mode {
	case api.ModeOff:
		err = lp.rampOff()
	case api.ModeNow:
		// ensure that new connections happen at min current
		current := lp.MinCurrent
		if lp.connected() {
			current = lp.MaxCurrent
		}
		err = lp.rampOn(current)
	case api.ModeMinPV, api.ModePV:
		// pv modes require meter measurements
		if meterErr != nil {
			log.WARN.Printf("%s aborting due to meter error", lp.Name)
			break
		}
		err = lp.updateModePV(mode)
	}

	if err != nil {
		log.ERROR.Println(err)
	}

	lp.publish("chargedEnergy", 1e3*lp.chargedEnergy()) // return Wh for UI
	lp.publish("chargeDuration", lp.chargeDuration())

	lp.publishSoC()

	return lp.chargePower
}
