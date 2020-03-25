package server

import (
	"fmt"
	"time"

	"github.com/andig/evcc/core"
)

// Piper is the interface that data flow plugins must implement
type Piper interface {
	Pipe(in <-chan core.Param) <-chan core.Param
}

type cacheItem struct {
	updated time.Time
	val     interface{}
}

// Deduplicator allows filtering of channel data by given criteria
type Deduplicator struct {
	interval time.Duration
	filter   map[string]interface{}
	cache    map[string]cacheItem
}

// cacheKey determines unique key for cached value lookup
func cacheKey(p core.Param) string {
	key := p.Key
	for k, v := range p.Tags {
		key += fmt.Sprintf(".%s=%s", k, v)
	}
	return key
}

// NewDeduplicator creates Deduplicator
func NewDeduplicator(interval time.Duration, filter ...string) Piper {
	l := &Deduplicator{
		interval: interval,
		filter:   make(map[string]interface{}),
		cache:    make(map[string]cacheItem),
	}

	for _, f := range filter {
		l.filter[f] = struct{}{}
	}

	return l
}

func (l *Deduplicator) pipe(in <-chan core.Param, out chan<- core.Param) {
	for p := range in {
		key := cacheKey(p)
		item, cached := l.cache[key]
		_, filtered := l.filter[p.Key]

		// forward if not cached
		if !cached || !filtered || filtered &&
			(time.Since(item.updated) >= l.interval || p.Val != item.val) {
			l.cache[key] = cacheItem{updated: time.Now(), val: p.Val}
			out <- p
		}
	}
}

// Pipe creates a new filtered output channel for given input channel
func (l *Deduplicator) Pipe(in <-chan core.Param) <-chan core.Param {
	out := make(chan core.Param)
	go l.pipe(in, out)
	return out
}

// Limiter allows filtering of channel data by given criteria
type Limiter struct {
	interval time.Duration
	cache    map[string]cacheItem
}

// NewLimiter creates limiter
func NewLimiter(interval time.Duration) Piper {
	l := &Limiter{
		interval: interval,
		cache:    make(map[string]cacheItem),
	}

	return l
}

func (l *Limiter) pipe(in <-chan core.Param, out chan<- core.Param) {
	for p := range in {
		key := cacheKey(p)
		item, cached := l.cache[key]

		// forward if not cached or expired
		if !cached || time.Since(item.updated) >= l.interval {
			l.cache[key] = cacheItem{updated: time.Now(), val: p.Val}
			out <- p
		}
	}
}

// Pipe creates a new filtered output channel for given input channel
func (l *Limiter) Pipe(in <-chan core.Param) <-chan core.Param {
	out := make(chan core.Param)
	go l.pipe(in, out)
	return out
}

// Splitter allows filtering of channel data by given criteria
type Splitter struct {
	key   string
	value float64
	tags  []string
}

// NewSplitter creates Splitter
func NewSplitter(key string, value float64, tags ...string) Piper {
	l := &Splitter{
		key:   key,
		value: value,
		tags:  tags,
	}

	return l
}

func (l *Splitter) pipe(in <-chan core.Param, out chan<- core.Param) {
	for p := range in {
		if p.Key != l.key {
			out <- p
			continue
		}

		pplus := p
		pminus := p

		// clone map
		pminus.Tags = make(map[string]string)
		for k, v := range p.Tags {
			pminus.Tags[k] = v
		}

		tag := l.tags[0]
		pplus.Tags[tag] = l.tags[1]  // use first tag value
		pminus.Tags[tag] = l.tags[2] // use second tag value

		if p.Val.(float64) >= 0 {
			pminus.Val = 0
		} else {
			pplus.Val = 0
		}

		out <- pplus
		out <- pminus
	}
}

// Pipe creates a new filtered output channel for given input channel
func (l *Splitter) Pipe(in <-chan core.Param) <-chan core.Param {
	out := make(chan core.Param)
	go l.pipe(in, out)
	return out
}
