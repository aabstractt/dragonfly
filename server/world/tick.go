package world

import (
	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/internal/sliceutil"
	"golang.org/x/exp/slices"
	"math/rand"
	"time"
)

// ticker implements World ticking methods. World embeds this struct, so any exported methods on ticker are exported
// methods on World.
type ticker struct{}

// tickLoop starts ticking the World 20 times every second, updating all entities, blocks and other features such as
// the time and weather of the World, as required.
func tickLoop(w *World) {
	tc := time.NewTicker(time.Second / 20)
	defer tc.Stop()

	w.running.Add(1)
	var t ticker
	for {
		select {
		case <-tc.C:
			w.Query(t.tick)
		case <-w.closing:
			// World is being closed: Stop ticking and get rid of a task.
			w.running.Done()
			return
		}
	}
}

// tick performs a tick on the World and updates the time, weather, blocks and entities that require updates.
func (t ticker) tick(txn *Tx) {
	viewers, loaders := txn.w.allViewers()

	txn.w.set.Lock()
	if len(viewers) == 0 && txn.w.set.CurrentTick != 0 {
		txn.w.set.Unlock()
		return
	}
	if txn.w.advance {
		txn.w.set.CurrentTick++
		if txn.w.set.TimeCycle {
			txn.w.set.Time++
		}
		if txn.w.set.WeatherCycle {
			txn.w.advanceWeather()
		}
	}

	rain, thunder, tick, tim := txn.w.set.Raining, txn.w.set.Thundering && txn.w.set.Raining, txn.w.set.CurrentTick, int(txn.w.set.Time)
	txn.w.set.Unlock()

	if tick%20 == 0 {
		for _, viewer := range viewers {
			if txn.w.conf.Dim.TimeCycle() {
				viewer.ViewTime(tim)
			}
			if txn.w.conf.Dim.WeatherCycle() {
				viewer.ViewWeather(rain, thunder)
			}
		}
	}
	if thunder {
		txn.w.tickLightning()
	}

	t.tickEntities(tick, txn)
	t.tickBlocksRandomly(loaders, tick, txn)
	t.tickScheduledBlocks(tick, txn)
	t.performNeighbourUpdates(txn)
}

// tickScheduledBlocks executes scheduled block updates in chunks that are currently loaded.
func (t ticker) tickScheduledBlocks(tick int64, txn *Tx) {
	for pos, scheduledTick := range txn.w.scheduledUpdates {
		if scheduledTick > tick {
			continue
		}
		delete(txn.w.scheduledUpdates, pos)
		if ticker, ok := txn.w.block(pos).(ScheduledTicker); ok {
			ticker.ScheduledTick(pos, txn, txn.w.r)
		}
		if liquid, ok := txn.w.additionalLiquid(pos); ok {
			if ticker, ok := liquid.(ScheduledTicker); ok {
				ticker.ScheduledTick(pos, txn, txn.w.r)
			}
		}
	}
}

// performNeighbourUpdates performs all block updates that came as a result of a neighbouring block being changed.
func (ticker) performNeighbourUpdates(txn *Tx) {
	for _, update := range txn.w.neighbourUpdates {
		pos, changedNeighbour := update.pos, update.neighbour
		if ticker, ok := txn.w.block(pos).(NeighbourUpdateTicker); ok {
			ticker.NeighbourUpdateTick(pos, changedNeighbour, txn)
		}
		if liquid, ok := txn.w.additionalLiquid(pos); ok {
			if ticker, ok := liquid.(NeighbourUpdateTicker); ok {
				ticker.NeighbourUpdateTick(pos, changedNeighbour, txn)
			}
		}
	}
	txn.w.neighbourUpdates = txn.w.neighbourUpdates[:0]
}

// tickBlocksRandomly executes random block ticks in each sub chunk in the World that has at least one viewer
// registered from the viewers passed.
func (t ticker) tickBlocksRandomly(loaders []*Loader, tick int64, txn *Tx) {
	r := int32(txn.w.tickRange())
	if r == 0 {
		// NOP if the simulation distance is 0.
		return
	}
	var g randUint4

	loaded := make([]ChunkPos, 0, len(loaders))
	for _, loader := range loaders {
		loaded = append(loaded, loader.pos)
	}

	for pos, c := range txn.w.chunks {
		if !t.anyWithinDistance(pos, loaded, r) {
			// No loaders in this chunk that are within the simulation distance, so proceed to the next.
			continue
		}

		for bpos := range c.e {
			if tb, ok := txn.Block(bpos).(TickerBlock); ok {
				tb.Tick(tick, bpos, txn)
			}
		}

		cx, cz := int(pos[0]<<4), int(pos[1]<<4)

		// We generate up to j random positions for every sub chunk.
		for j := 0; j < txn.w.conf.RandomTickSpeed; j++ {
			x, y, z := g.uint4(txn.w.r), g.uint4(txn.w.r), g.uint4(txn.w.r)

			for i, sub := range c.Sub() {
				if sub.Empty() {
					// SubChunk is empty, so skip it right away.
					continue
				}
				// Generally we would want to make sure the block has its block entities, but provided blocks
				// with block entities are generally ticked already, we are safe to assume that blocks
				// implementing the RandomTicker don't rely on additional block entity data.
				if rid := sub.Layers()[0].At(x, y, z); randomTickBlocks[rid] {
					subY := (i + (txn.w.ra.Min() >> 4)) << 4

					bpos := cube.Pos{cx + int(x), subY + int(y), cz + int(z)}
					if rb, ok := txn.Block(bpos).(RandomTicker); ok {
						rb.RandomTick(bpos, txn, txn.w.r)
					}

					// Only generate new coordinates if a tickable block was actually found. If not, we can just re-use
					// the coordinates for the next sub chunk.
					x, y, z = g.uint4(txn.w.r), g.uint4(txn.w.r), g.uint4(txn.w.r)
				}
			}
		}
	}
}

// anyWithinDistance checks if any of the ChunkPos loaded are within the distance r of the ChunkPos pos.
func (t ticker) anyWithinDistance(pos ChunkPos, loaded []ChunkPos, r int32) bool {
	for _, chunkPos := range loaded {
		xDiff, zDiff := chunkPos[0]-pos[0], chunkPos[1]-pos[1]
		if (xDiff*xDiff)+(zDiff*zDiff) <= r*r {
			// The chunk was within the simulation distance of at least one viewer, so we can proceed to
			// ticking the block.
			return true
		}
	}
	return false
}

// tickEntities ticks all entities in the World, making sure they are still located in the correct chunks and
// updating where necessary.
func (ticker) tickEntities(tick int64, txn *Tx) {
	type entityToMove struct {
		e             Entity
		after         *chunkData
		viewersBefore []Viewer
	}
	var entitiesToMove []entityToMove

	for e, lastPos := range txn.w.entities {
		chunkPos := chunkPosFromVec3(e.Position())

		c, ok := txn.w.chunks[chunkPos]
		if !ok {
			continue
		}
		if len(c.v) > 0 {
			if te, ok := e.(TickerEntity); ok && te.World() == txn.w {
				te.Tick(txn, tick)
			}
		}

		if lastPos != chunkPos {
			// The entity was stored using an outdated chunk position. We update it and make sure it is ready
			// for loaders to view it.
			txn.w.entities[e] = chunkPos
			var viewers []Viewer

			// When changing an entity's World, then teleporting it immediately, we could end up in a situation
			// where the old chunk of the entity was not loaded. In this case, it should be safe simply to ignore
			// the loaders from the old chunk. We can assume they never saw the entity in the first place.
			if old, ok := txn.w.chunks[lastPos]; ok {
				old.entities = sliceutil.DeleteVal(old.entities, e)
				viewers = slices.Clone(old.v)
			}
			entitiesToMove = append(entitiesToMove, entityToMove{e: e, viewersBefore: viewers, after: c})
		}
	}

	for _, move := range entitiesToMove {
		move.after.entities = append(move.after.entities, move.e)
		viewersAfter := move.after.v

		for _, viewer := range move.viewersBefore {
			if sliceutil.Index(viewersAfter, viewer) == -1 {
				// First we hide the entity from all loaders that were previously viewing it, but no
				// longer are.
				viewer.HideEntity(move.e)
			}
		}
		for _, viewer := range viewersAfter {
			if sliceutil.Index(move.viewersBefore, viewer) == -1 {
				// Then we show the entity to all loaders that are now viewing the entity in the new
				// chunk.
				showEntity(move.e, viewer)
			}
		}
	}
}

// randUint4 is a structure used to generate random uint4s.
type randUint4 struct {
	x uint64
	n uint8
}

// uint4 returns a random uint4.
func (g *randUint4) uint4(r *rand.Rand) uint8 {
	if g.n == 0 {
		g.x = r.Uint64()
		g.n = 16
	}
	val := g.x & 0b1111

	g.x >>= 4
	g.n--
	return uint8(val)
}
