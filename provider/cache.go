package provider

import (
	"time"

	"github.com/andig/evcc/util"
	"github.com/benbjohnson/clock"
)

// Cached wraps a getter with a cache
type Cached struct {
	log     *util.Logger
	clock   clock.Clock
	updated time.Time
	cache   time.Duration
	getter  interface{}
	val     interface{}
	err     error
}

// NewCached wraps a getter with a cache
func NewCached(log *util.Logger, getter interface{}, cache time.Duration) *Cached {
	return &Cached{
		log:    log,
		clock:  clock.New(),
		getter: getter,
		cache:  cache,
	}
}

// FloatGetter gets float value
func (c *Cached) FloatGetter() FloatGetter {
	g, ok := c.getter.(func() (float64, error))
	if !ok {
		c.log.FATAL.Fatalf("invalid type: %T", c.getter)
	}

	return FloatGetter(func() (float64, error) {
		if c.clock.Since(c.updated) > c.cache {
			c.val, c.err = g()
			c.updated = c.clock.Now()
		}

		return c.val.(float64), c.err
	})
}

// IntGetter gets int value
func (c *Cached) IntGetter() IntGetter {
	g, ok := c.getter.(func() (int64, error))
	if !ok {
		c.log.FATAL.Fatalf("invalid type: %T", c.getter)
	}

	return IntGetter(func() (int64, error) {
		if c.clock.Since(c.updated) > c.cache {
			c.val, c.err = g()
			c.updated = c.clock.Now()
		}

		return c.val.(int64), c.err
	})
}

// StringGetter gets string value
func (c *Cached) StringGetter() StringGetter {
	g, ok := c.getter.(func() (string, error))
	if !ok {
		c.log.FATAL.Fatalf("invalid type: %T", c.getter)
	}

	return StringGetter(func() (string, error) {
		if c.clock.Since(c.updated) > c.cache {
			c.val, c.err = g()
			c.updated = c.clock.Now()
		}

		return c.val.(string), c.err
	})
}

// BoolGetter gets bool value
func (c *Cached) BoolGetter() BoolGetter {
	g, ok := c.getter.(func() (bool, error))
	if !ok {
		c.log.FATAL.Fatalf("invalid type: %T", g)
	}

	return BoolGetter(func() (bool, error) {
		if c.clock.Since(c.updated) > c.cache {
			c.val, c.err = g()
			c.updated = c.clock.Now()
		}

		return c.val.(bool), c.err
	})
}
