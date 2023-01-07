package world

import (
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/df-mc/atomic"
	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/event"
	"github.com/df-mc/dragonfly/server/internal/sliceutil"
	"github.com/df-mc/dragonfly/server/world/chunk"
	"github.com/go-gl/mathgl/mgl64"
	"github.com/google/uuid"
	"golang.org/x/exp/maps"
	"golang.org/x/exp/slices"
)

// World implements a Minecraft World. It manages all aspects of what players can see, such as blocks,
// entities and particles.
// World generally provides a synchronised state: All entities, blocks and players usually operate in this
// World, so World ensures that all its methods will always be safe for simultaneous calls.
// A nil *World is safe to use but not functional.
type World struct {
	conf Config
	ra   cube.Range
	// advance is a bool that specifies if this World should advance the current tick, time and weather saved in the
	// Settings struct held by the World.
	advance bool

	o sync.Once
	q chan QueryFunc

	set     *Settings
	handler atomic.Value[Handler]

	weather

	closing chan struct{}
	running sync.WaitGroup

	// chunks holds a cache of chunks currently loaded. These chunks are cleared from this map after some time
	// of not being used.
	chunks map[ChunkPos]*chunkData

	// entities holds a map of entities currently loaded and the last ChunkPos that the Entity was in.
	// These are tracked so that a call to removeEntity can find the correct entity.
	entities map[Entity]ChunkPos

	r *rand.Rand

	// scheduledUpdates is a map of tick time values indexed by the block position at which an update is
	// scheduled. If the current tick exceeds the tick value passed, the block update will be performed
	// and the entry will be removed from the map.
	scheduledUpdates map[cube.Pos]int64
	neighbourUpdates []neighbourUpdate

	viewersMu sync.Mutex
	viewers   map[*Loader]Viewer
}

// New creates a new initialised World. The World may be used right away, but it will not be saved or loaded
// from files until it has been given a different provider than the default. (NopProvider)
// By default, the name of the World will be 'World'.
func New() *World {
	var conf Config
	return conf.New()
}

// Name returns the display name of the World. Generally, this name is displayed at the top of the player list
// in the pause screen in-game.
// If a provider is set, the name will be updated according to the name that it provides.
func (w *World) Name() string {
	w.set.Lock()
	defer w.set.Unlock()
	return w.set.Name
}

// Dimension returns the Dimension assigned to the World in world.New. The sky colour and behaviour of a variety of
// World features differ based on the Dimension assigned to a World.
func (w *World) Dimension() Dimension {
	if w == nil {
		return nopDim{}
	}
	return w.conf.Dim
}

// Range returns the range in blocks of the World (min and max). It is equivalent to calling World.Dimension().Range().
func (w *World) Range() cube.Range {
	if w == nil {
		return cube.Range{}
	}
	return w.ra
}

type QueryFunc func(tx *Tx)

func (w *World) Query(f QueryFunc) {
	w.q <- f
}

func (w *World) run() {
	for f := range w.q {
		txn := &Tx{w: w}
		f(txn)
		txn.complete.Store(true)
	}
}

// EntityRegistry returns the EntityRegistry that was passed to the World's
// Config upon construction.
func (w *World) EntityRegistry() EntityRegistry {
	return w.conf.Entities
}

// Block reads a block from the position passed. If a chunk is not yet loaded at that position, the chunk is
// loaded, or generated if it could not be found in the World save, and the block returned. Chunks will be
// loaded synchronously.
func (w *World) block(pos cube.Pos) Block {
	if pos.OutOfBounds(w.ra) {
		// Fast way out.
		return air()
	}
	c := w.chunk(chunkPosFromBlockPos(pos))

	rid := c.Block(uint8(pos[0]), int16(pos[1]), uint8(pos[2]), 0)
	if nbtBlocks[rid] {
		// The block was also a block entity, so we look it up in the block entity map.
		if nbtB, ok := c.e[pos]; ok {
			return nbtB
		}
	}
	b, _ := BlockByRuntimeID(rid)
	return b
}

// Biome reads the biome at the position passed. If a chunk is not yet loaded at that position, the chunk is
// loaded, or generated if it could not be found in the World save, and the biome returned. Chunks will be
// loaded synchronously.
func (w *World) biome(pos cube.Pos) Biome {
	if pos.OutOfBounds(w.ra) {
		// Fast way out.
		return ocean()
	}
	c := w.chunk(chunkPosFromBlockPos(pos))

	id := int(c.Biome(uint8(pos[0]), int16(pos[1]), uint8(pos[2])))
	b, ok := BiomeByID(id)
	if !ok {
		w.conf.Log.Errorf("could not find biome by ID %v", id)
	}
	return b
}

// blockInChunk reads a block from the World at the position passed. The block is assumed to be in the chunk
// passed, which is also assumed to be locked already or otherwise not yet accessible.
func (w *World) blockInChunk(c *chunkData, pos cube.Pos) Block {
	if pos.OutOfBounds(w.ra) {
		// Fast way out.
		return air()
	}
	rid := c.Block(uint8(pos[0]), int16(pos[1]), uint8(pos[2]), 0)
	if nbtBlocks[rid] {
		// The block was also a block entity, so we look it up in the block entity map.
		if b, ok := c.e[pos]; ok {
			return b
		}
	}
	b, _ := BlockByRuntimeID(rid)
	return b
}

// highestLightBlocker gets the Y value of the highest fully light blocking block at the x and z values
// passed in the World.
func (w *World) highestLightBlocker(x, z int) int {
	if w == nil {
		return w.ra[0]
	}
	c := w.chunk(ChunkPos{int32(x >> 4), int32(z >> 4)})
	return int(c.HighestLightBlocker(uint8(x), uint8(z)))
}

// highestBlock looks up the highest non-air block in the World at a specific x and z in the World. The y
// value of the highest block is returned, or 0 if no blocks were present in the column.
func (w *World) highestBlock(x, z int) int {
	if w == nil {
		return w.ra[0]
	}
	c := w.chunk(ChunkPos{int32(x >> 4), int32(z >> 4)})
	return int(c.HighestBlock(uint8(x), uint8(z)))
}

// highestObstructingBlock returns the highest block in the World at a given x and z that has at least a solid top or
// bottom face.
func (w *World) highestObstructingBlock(x, z int) int {
	// Create a temporary transaction, because the FaceSolid method needs one.
	txn := &Tx{w: w}

	min := w.ra[0]
	yHigh := w.highestBlock(x, z)
	for y := yHigh; y >= min; y-- {
		pos := cube.Pos{x, y, z}
		m := w.block(pos).Model()
		if m.FaceSolid(pos, cube.FaceUp, txn) || m.FaceSolid(pos, cube.FaceDown, txn) {
			return y
		}
	}
	return min
}

// SetOpts holds several parameters that may be set to disable updates in the World of different kinds as a result of
// a call to setBlock.
type SetOpts struct {
	// DisableBlockUpdates makes setBlock not update any neighbouring blocks as a result of the setBlock call.
	DisableBlockUpdates bool
	// DisableLiquidDisplacement disables the displacement of liquid blocks to the second layer (or back to the first
	// layer, if it already was on the second layer). Disabling this is not strongly recommended unless performance is
	// very important or where it is known no liquid can be present anyway.
	DisableLiquidDisplacement bool
}

// setBlock writes a block to the position passed. If a chunk is not yet loaded at that position, the chunk is
// first loaded or generated if it could not be found in the World save.
// setBlock panics if the block passed has not yet been registered using RegisterBlock().
// Nil may be passed as the block to set the block to air.
//
// A SetOpts struct may be passed to additionally modify behaviour of setBlock, specifically to improve performance
// under specific circumstances. Nil should be passed where performance is not essential, to make sure the World is
// updated adequately.
//
// setBlock should be avoided in situations where performance is critical when needing to set a lot of blocks
// to the World. buildStructure may be used instead.
func (w *World) setBlock(pos cube.Pos, b Block, opts *SetOpts) {
	if pos.OutOfBounds(w.ra) {
		// Fast way out.
		return
	}
	if opts == nil {
		opts = &SetOpts{}
	}

	x, y, z := uint8(pos[0]), int16(pos[1]), uint8(pos[2])
	c := w.chunk(chunkPosFromBlockPos(pos))

	rid := BlockRuntimeID(b)

	var before uint32
	if rid != airRID && !opts.DisableLiquidDisplacement {
		before = c.Block(x, y, z, 0)
	}

	c.m = true
	c.SetBlock(x, y, z, 0, rid)
	if nbtBlocks[rid] {
		c.e[pos] = b
	} else {
		delete(c.e, pos)
	}

	if !opts.DisableLiquidDisplacement {
		var secondLayer Block

		if rid == airRID {
			if li := c.Block(x, y, z, 1); li != airRID {
				c.SetBlock(x, y, z, 0, li)
				c.SetBlock(x, y, z, 1, airRID)
				secondLayer = air()
				b, _ = BlockByRuntimeID(li)
			}
		} else if liquidDisplacingBlocks[rid] && liquidBlocks[before] {
			l, _ := BlockByRuntimeID(before)
			if b.(LiquidDisplacer).CanDisplace(l.(Liquid)) {
				c.SetBlock(x, y, z, 1, before)
				secondLayer = l
			}
		}
		if secondLayer != nil {
			for _, viewer := range c.v {
				viewer.ViewBlockUpdate(pos, secondLayer, 1)
			}
		}
	}

	for _, viewer := range c.v {
		viewer.ViewBlockUpdate(pos, b, 0)
	}

	if !opts.DisableBlockUpdates {
		w.doBlockUpdatesAround(pos)
	}
}

// setBiome sets the biome at the position passed. If a chunk is not yet loaded at that position, the chunk is
// first loaded or generated if it could not be found in the World save.
func (w *World) setBiome(pos cube.Pos, b Biome) {
	if pos.OutOfBounds(w.ra) {
		// Fast way out.
		return
	}
	c := w.chunk(chunkPosFromBlockPos(pos))
	c.m = true
	c.SetBiome(uint8(pos[0]), int16(pos[1]), uint8(pos[2]), uint32(b.EncodeBiome()))
}

// buildStructure builds a Structure passed at a specific position in the World. Unlike setBlock, it takes a
// Structure implementation, which provides blocks to be placed at a specific location.
// buildStructure is specifically tinkered to be able to process a large batch of chunks simultaneously and
// will do so within much less time than separate setBlock calls would.
// The method operates on a per-chunk basis, setting all blocks within a single chunk part of the structure
// before moving on to the next chunk.
func (w *World) buildStructure(pos cube.Pos, s Structure) {
	dim := s.Dimensions()
	width, height, length := dim[0], dim[1], dim[2]
	maxX, maxY, maxZ := pos[0]+width, pos[1]+height, pos[2]+length

	for chunkX := pos[0] >> 4; chunkX <= maxX>>4; chunkX++ {
		for chunkZ := pos[2] >> 4; chunkZ <= maxZ>>4; chunkZ++ {
			// We approach this on a per-chunk basis, so that we can keep only one chunk in memory at a time
			// while not needing to acquire a new chunk lock for every block. This also allows us not to send
			// block updates, but instead send a single chunk update once.
			chunkPos := ChunkPos{int32(chunkX), int32(chunkZ)}
			c := w.chunk(chunkPos)
			f := func(x, y, z int) Block {
				actual := cube.Pos{pos[0] + x, pos[1] + y, pos[2] + z}
				if actual[0]>>4 == chunkX && actual[2]>>4 == chunkZ {
					return w.blockInChunk(c, actual)
				}
				return w.block(actual)
			}
			baseX, baseZ := chunkX<<4, chunkZ<<4
			subs := c.Sub()
			for i, sub := range subs {
				baseY := (i + (w.ra[0] >> 4)) << 4
				if baseY>>4 < pos[1]>>4 {
					continue
				} else if baseY >= maxY {
					break
				}

				for localY := 0; localY < 16; localY++ {
					yOffset := baseY + localY
					if yOffset > w.ra[1] || yOffset >= maxY {
						// We've hit the height limit for blocks.
						break
					} else if yOffset < w.ra[0] || yOffset < pos[1] {
						// We've got a block below the minimum, but other blocks might still reach above
						// it, so don't break but continue.
						continue
					}
					for localX := 0; localX < 16; localX++ {
						xOffset := baseX + localX
						if xOffset < pos[0] || xOffset >= maxX {
							continue
						}
						for localZ := 0; localZ < 16; localZ++ {
							zOffset := baseZ + localZ
							if zOffset < pos[2] || zOffset >= maxZ {
								continue
							}
							b, liq := s.At(xOffset-pos[0], yOffset-pos[1], zOffset-pos[2], f)
							if b != nil {
								rid := BlockRuntimeID(b)
								sub.SetBlock(uint8(xOffset), uint8(yOffset), uint8(zOffset), 0, rid)

								nbtPos := cube.Pos{xOffset, yOffset, zOffset}
								if nbtBlocks[rid] {
									c.e[nbtPos] = b
								} else {
									delete(c.e, nbtPos)
								}
							}
							if liq != nil {
								sub.SetBlock(uint8(xOffset), uint8(yOffset), uint8(zOffset), 1, BlockRuntimeID(liq))
							} else if len(sub.Layers()) > 1 {
								sub.SetBlock(uint8(xOffset), uint8(yOffset), uint8(zOffset), 1, airRID)
							}
						}
					}
				}
			}
			c.SetBlock(0, 0, 0, 0, c.Block(0, 0, 0, 0)) // Make sure the heightmap is recalculated.
			c.m = true

			// After setting all blocks of the structure within a single chunk, we show the new chunk to all
			// viewers once, and unlock it.
			for _, viewer := range c.v {
				viewer.ViewChunk(chunkPos, c.Chunk, c.e)
			}
		}
	}
}

// Liquid attempts to return any liquid block at the position passed. This liquid may be in the foreground or
// in any other layer.
// If found, the liquid is returned. If not, the bool returned is false and the liquid is nil.
func (w *World) liquid(pos cube.Pos) (Liquid, bool) {
	if pos.OutOfBounds(w.ra) {
		// Fast way out.
		return nil, false
	}
	c := w.chunk(chunkPosFromBlockPos(pos))
	x, y, z := uint8(pos[0]), int16(pos[1]), uint8(pos[2])

	id := c.Block(x, y, z, 0)
	b, ok := BlockByRuntimeID(id)
	if !ok {
		w.conf.Log.Errorf("failed getting liquid: cannot get block by runtime ID %v", id)
		return nil, false
	}
	if liq, ok := b.(Liquid); ok {
		return liq, true
	}
	id = c.Block(x, y, z, 1)

	b, ok = BlockByRuntimeID(id)
	if !ok {
		w.conf.Log.Errorf("failed getting liquid: cannot get block by runtime ID %v", id)
		return nil, false
	}
	liq, ok := b.(Liquid)
	return liq, ok
}

// setLiquid sets the liquid at a specific position in the World. Unlike setBlock, setLiquid will not
// overwrite any existing blocks. It will instead be in the same position as a block currently there, unless
// there already is a liquid at that position, in which case it will be overwritten.
// If nil is passed for the liquid, any liquid currently present will be removed.
func (w *World) setLiquid(pos cube.Pos, b Liquid) {
	if pos.OutOfBounds(w.ra) {
		// Fast way out.
		return
	}
	chunkPos := chunkPosFromBlockPos(pos)
	c := w.chunk(chunkPos)
	if b == nil {
		w.removeLiquids(c, pos)
		w.doBlockUpdatesAround(pos)
		return
	}
	x, y, z := uint8(pos[0]), int16(pos[1]), uint8(pos[2])
	if !replaceable(w, c, pos, b) {
		if displacer, ok := w.blockInChunk(c, pos).(LiquidDisplacer); !ok || !displacer.CanDisplace(b) {
			return
		}
	}
	rid := BlockRuntimeID(b)
	if w.removeLiquids(c, pos) {
		c.SetBlock(x, y, z, 0, rid)
		for _, v := range c.v {
			v.ViewBlockUpdate(pos, b, 0)
		}
	} else {
		c.SetBlock(x, y, z, 1, rid)
		for _, v := range c.v {
			v.ViewBlockUpdate(pos, b, 1)
		}
	}
	c.m = true
	w.doBlockUpdatesAround(pos)
}

// removeLiquids removes any liquid blocks that may be present at a specific block position in the chunk
// passed.
// The bool returned specifies if no blocks were left on the foreground layer.
func (w *World) removeLiquids(c *chunkData, pos cube.Pos) bool {
	x, y, z := uint8(pos[0]), int16(pos[1]), uint8(pos[2])

	noneLeft := false
	if noLeft, changed := w.removeLiquidOnLayer(c.Chunk, x, y, z, 0); noLeft {
		if changed {
			for _, v := range c.v {
				v.ViewBlockUpdate(pos, air(), 0)
			}
		}
		noneLeft = true
	}
	if _, changed := w.removeLiquidOnLayer(c.Chunk, x, y, z, 1); changed {
		for _, v := range c.v {
			v.ViewBlockUpdate(pos, air(), 1)
		}
	}
	return noneLeft
}

// removeLiquidOnLayer removes a liquid block from a specific layer in the chunk passed, returning true if
// successful.
func (w *World) removeLiquidOnLayer(c *chunk.Chunk, x uint8, y int16, z, layer uint8) (bool, bool) {
	id := c.Block(x, y, z, layer)

	b, ok := BlockByRuntimeID(id)
	if !ok {
		w.conf.Log.Errorf("failed removing liquids: cannot get block by runtime ID %v", id)
		return false, false
	}
	if _, ok := b.(Liquid); ok {
		c.SetBlock(x, y, z, layer, airRID)
		return true, true
	}
	return id == airRID, false
}

// additionalLiquid checks if the block at a position has additional liquid on another layer and returns the
// liquid if so.
func (w *World) additionalLiquid(pos cube.Pos) (Liquid, bool) {
	if pos.OutOfBounds(w.ra) {
		// Fast way out.
		return nil, false
	}
	c := w.chunk(chunkPosFromBlockPos(pos))
	id := c.Block(uint8(pos[0]), int16(pos[1]), uint8(pos[2]), 1)
	b, ok := BlockByRuntimeID(id)
	if !ok {
		w.conf.Log.Errorf("failed getting liquid: cannot get block by runtime ID %v", id)
		return nil, false
	}
	liq, ok := b.(Liquid)
	return liq, ok
}

// light returns the light level at the position passed. This is the highest of the sky and block light.
// The light value returned is a value in the range 0-15, where 0 means there is no light present, whereas
// 15 means the block is fully lit.
func (w *World) light(pos cube.Pos) uint8 {
	if pos[1] < w.ra[0] {
		// Fast way out.
		return 0
	} else if pos[1] > w.ra[1] {
		// Above the rest of the World, so full skylight.
		return 15
	}
	c := w.chunk(chunkPosFromBlockPos(pos))
	return c.Light(uint8(pos[0]), int16(pos[1]), uint8(pos[2]))
}

// skylight returns the skylight level at the position passed. This light level is not influenced by blocks
// that emit light, such as torches or glowstone. The light value, similarly to light, is a value in the
// range 0-15, where 0 means no light is present.
func (w *World) skylight(pos cube.Pos) uint8 {
	if pos[1] < w.ra[0] {
		// Fast way out.
		return 0
	} else if pos[1] > w.ra[1] {
		// Above the rest of the World, so full skylight.
		return 15
	}
	c := w.chunk(chunkPosFromBlockPos(pos))
	return c.SkyLight(uint8(pos[0]), int16(pos[1]), uint8(pos[2]))
}

// Time returns the current time of the World. The time is incremented every 1/20th of a second, unless
// World.StopTime() is called.
func (w *World) Time() int {
	if w == nil {
		return 0
	}
	w.set.Lock()
	defer w.set.Unlock()
	return int(w.set.Time)
}

// SetTime sets the new time of the World. SetTime will always work, regardless of whether the time is stopped
// or not.
func (w *World) SetTime(new int) {
	if w == nil {
		return
	}
	w.set.Lock()
	w.set.Time = int64(new)
	w.set.Unlock()

	viewers, _ := w.allViewers()
	for _, viewer := range viewers {
		viewer.ViewTime(new)
	}
}

// StopTime stops the time in the World. When called, the time will no longer cycle and the World will remain
// at the time when StopTime is called. The time may be restarted by calling World.StartTime().
// StopTime will not do anything if the time is already stopped.
func (w *World) StopTime() {
	w.enableTimeCycle(false)
}

// StartTime restarts the time in the World. When called, the time will start cycling again and the day/night
// cycle will continue. The time may be stopped again by calling World.StopTime().
// StartTime will not do anything if the time is already started.
func (w *World) StartTime() {
	w.enableTimeCycle(true)
}

// enableTimeCycle enables or disables the time cycling of the World.
func (w *World) enableTimeCycle(v bool) {
	if w == nil {
		return
	}
	w.set.Lock()
	defer w.set.Unlock()
	w.set.TimeCycle = v
}

// temperature returns the temperature in the World at a specific position. Higher altitudes and different biomes
// influence the temperature returned.
func (w *World) temperature(pos cube.Pos) float64 {
	const (
		tempDrop = 1.0 / 600
		seaLevel = 64
	)
	diff := pos[1] - seaLevel
	if diff < 0 {
		diff = 0
	}
	return w.biome(pos).Temperature() - float64(diff)*tempDrop
}

// addParticle spawns a particle at a given position in the World. Viewers that are viewing the chunk will be
// shown the particle.
func (w *World) addParticle(pos mgl64.Vec3, p Particle) {
	if w == nil {
		return
	}
	p.Spawn(w, pos)
	for _, viewer := range w.Viewers(pos) {
		viewer.ViewParticle(pos, p)
	}
}

// playSound plays a sound at a specific position in the World. Viewers of that position will be able to hear
// the sound if they're close enough.
func (w *World) playSound(pos mgl64.Vec3, s Sound) {
	ctx := event.C()
	if w.Handler().HandleSound(ctx, s, pos); ctx.Cancelled() {
		return
	}
	for _, viewer := range w.Viewers(pos) {
		viewer.ViewSound(pos, s)
	}
}

var (
	worldsMu sync.RWMutex
	// entityWorlds holds a list of all entities added to a World. It may be used to look up the World that an
	// entity is currently in.
	entityWorlds = map[Entity]*World{}
)

// addEntity adds an entity to the World at the position that the entity has. The entity will be visible to
// all viewers of the World that have the chunk of the entity loaded.
// If the chunk that the entity is in is not yet loaded, it will first be loaded.
// If the entity passed to addEntity is currently in a World, it is first removed from that World.
func (w *World) addEntity(e Entity) {
	// Remove the Entity from any previous World it might be in.
	e.World().removeEntity(e)

	add(e, w)

	chunkPos := chunkPosFromVec3(e.Position())
	w.entities[e] = chunkPos

	c := w.chunk(chunkPos)
	c.entities = append(c.entities, e)
	for _, v := range c.v {
		// We show the entity to all viewers currently in the chunk that the entity is spawned in.
		showEntity(e, v)
	}

	w.Handler().HandleEntitySpawn(e)
}

// add maps an Entity to a World in the entityWorlds map.
func add(e Entity, w *World) {
	worldsMu.Lock()
	entityWorlds[e] = w
	worldsMu.Unlock()
}

// removeEntity removes an entity from the World that is currently present in it. Any viewers of the entity
// will no longer be able to see it.
// removeEntity operates assuming the position of the entity is the same as where it is currently in the
// World. If it can not find it there, it will loop through all entities and try to find it.
// removeEntity assumes the entity is currently loaded and in a loaded chunk. If not, the function will not do
// anything.
func (w *World) removeEntity(e Entity) {
	pos, ok := w.entities[e]
	if !ok {
		// The entity currently isn't in this World.
		return
	}

	w.Handler().HandleEntityDespawn(e)

	worldsMu.Lock()
	delete(entityWorlds, e)
	worldsMu.Unlock()

	c, ok := w.chunks[pos]
	if !ok {
		return
	}
	c.entities = sliceutil.DeleteVal(c.entities, e)
	delete(w.entities, e)
	for _, v := range c.v {
		v.HideEntity(e)
	}
}

// entitiesWithin does a lookup through the entities in the chunks touched by the BBox passed, returning all
// those which are contained within the BBox when it comes to their position.
func (w *World) entitiesWithin(box cube.BBox, ignored func(Entity) bool) []Entity {
	// Make an estimate of 16 entities on average.
	m := make([]Entity, 0, 16)

	minPos, maxPos := chunkPosFromVec3(box.Min()), chunkPosFromVec3(box.Max())

	for x := minPos[0]; x <= maxPos[0]; x++ {
		for z := minPos[1]; z <= maxPos[1]; z++ {
			c, ok := w.chunks[ChunkPos{x, z}]
			if !ok {
				continue
			}
			for _, entity := range c.entities {
				if ignored != nil && ignored(entity) {
					continue
				}
				if box.Vec3Within(entity.Position()) {
					// The entity position was within the BBox, so we add it to the slice to return.
					m = append(m, entity)
				}
			}
		}
	}
	return m
}

// allEntities returns a list of all entities currently added to the World.
func (w *World) allEntities() []Entity {
	m := make([]Entity, 0, len(w.entities))
	for e := range w.entities {
		m = append(m, e)
	}
	return m
}

// OfEntity attempts to return a World that an entity is currently in. If the entity was not currently added
// to a World, the World returned is nil and the bool returned is false.
func OfEntity(e Entity) (*World, bool) {
	worldsMu.RLock()
	w, ok := entityWorlds[e]
	worldsMu.RUnlock()
	return w, ok
}

// Spawn returns the spawn of the World. Every new player will by default spawn on this position in the World
// when joining.
func (w *World) Spawn() cube.Pos {
	if w == nil {
		return cube.Pos{}
	}
	w.set.Lock()
	s := w.set.Spawn
	w.set.Unlock()
	if s[1] > w.ra[1] {
		s[1] = w.highestObstructingBlock(s[0], s[2]) + 1
	}
	return s
}

// SetSpawn sets the spawn of the World to a different position. The player will be spawned in the center of
// this position when newly joining.
func (w *World) SetSpawn(pos cube.Pos) {
	if w == nil {
		return
	}
	w.set.Lock()
	w.set.Spawn = pos
	w.set.Unlock()

	viewers, _ := w.allViewers()
	for _, viewer := range viewers {
		viewer.ViewWorldSpawn(pos)
	}
}

// PlayerSpawn returns the spawn position of a player with a UUID in this World.
func (w *World) PlayerSpawn(uuid uuid.UUID) cube.Pos {
	if w == nil {
		return cube.Pos{}
	}
	pos, exist, err := w.conf.Provider.LoadPlayerSpawnPosition(uuid)
	if err != nil {
		w.conf.Log.Errorf("failed to get player spawn: %v", err)
		return w.Spawn()
	}
	if !exist {
		return w.Spawn()
	}
	return pos
}

// SetPlayerSpawn sets the spawn position of a player with a UUID in this World. If the player has a spawn in the World,
// the player will be teleported to this location on respawn.
func (w *World) SetPlayerSpawn(uuid uuid.UUID, pos cube.Pos) {
	if w == nil {
		return
	}
	if err := w.conf.Provider.SavePlayerSpawnPosition(uuid, pos); err != nil {
		w.conf.Log.Errorf("failed to set player spawn: %v", err)
	}
}

// DefaultGameMode returns the default game mode of the World. When players join, they are given this game
// mode.
// The default game mode may be changed using SetDefaultGameMode().
func (w *World) DefaultGameMode() GameMode {
	if w == nil {
		return GameModeSurvival
	}
	w.set.Lock()
	defer w.set.Unlock()
	return w.set.DefaultGameMode
}

// SetTickRange sets the range in chunks around each Viewer that will have the chunks (their blocks and entities)
// ticked when the World is ticked.
func (w *World) SetTickRange(v int) {
	if w == nil {
		return
	}
	w.set.Lock()
	defer w.set.Unlock()
	w.set.TickRange = int32(v)
}

// tickRange returns the tick range around each Viewer.
func (w *World) tickRange() int {
	w.set.Lock()
	defer w.set.Unlock()
	return int(w.set.TickRange)
}

// SetDefaultGameMode changes the default game mode of the World. When players join, they are then given that
// game mode.
func (w *World) SetDefaultGameMode(mode GameMode) {
	if w == nil {
		return
	}
	w.set.Lock()
	defer w.set.Unlock()
	w.set.DefaultGameMode = mode
}

// Difficulty returns the difficulty of the World. Properties of mobs in the World and the player's hunger
// will depend on this difficulty.
func (w *World) Difficulty() Difficulty {
	if w == nil {
		return DifficultyNormal
	}
	w.set.Lock()
	defer w.set.Unlock()
	return w.set.Difficulty
}

// SetDifficulty changes the difficulty of a World.
func (w *World) SetDifficulty(d Difficulty) {
	if w == nil {
		return
	}
	w.set.Lock()
	defer w.set.Unlock()
	w.set.Difficulty = d
}

// scheduleBlockUpdate schedules a block update at the position passed after a specific delay. If the block at
// that position does not handle block updates, nothing will happen.
func (w *World) scheduleBlockUpdate(pos cube.Pos, delay time.Duration) {
	if pos.OutOfBounds(w.ra) {
		return
	}
	if _, exists := w.scheduledUpdates[pos]; exists {
		return
	}
	w.set.Lock()
	t := w.set.CurrentTick
	w.set.Unlock()

	w.scheduledUpdates[pos] = t + delay.Nanoseconds()/int64(time.Second/20)
}

// doBlockUpdatesAround schedules block updates directly around and on the position passed.
func (w *World) doBlockUpdatesAround(pos cube.Pos) {
	if pos.OutOfBounds(w.ra) {
		return
	}

	w.updateNeighbour(pos, pos)
	pos.Neighbours(func(neighbour cube.Pos) {
		w.updateNeighbour(neighbour, pos)
	}, w.ra)
}

// neighbourUpdate represents a position that needs to be updated because of a neighbour that changed.
type neighbourUpdate struct {
	pos, neighbour cube.Pos
}

// updateNeighbour ticks the position passed as a result of the neighbour passed being updated.
func (w *World) updateNeighbour(pos, changedNeighbour cube.Pos) {
	w.neighbourUpdates = append(w.neighbourUpdates, neighbourUpdate{pos: pos, neighbour: changedNeighbour})
}

// Handle changes the current Handler of the World. As a result, events called by the World will call
// handlers of the Handler passed.
// Handle sets the World's Handler to NopHandler if nil is passed.
func (w *World) Handle(h Handler) {
	if w == nil {
		return
	}
	if h == nil {
		h = NopHandler{}
	}
	w.handler.Store(h)
}

// Viewers returns a list of all viewers viewing the position passed. A viewer will be assumed to be watching
// if the position is within one of the chunks that the viewer is watching.
func (w *World) Viewers(pos mgl64.Vec3) (viewers []Viewer) {
	c, ok := w.chunks[chunkPosFromVec3(pos)]
	if !ok {
		return nil
	}
	return slices.Clone(c.v)
}

// PortalDestination returns the destination World for a portal of a specific Dimension. If no destination World could
// be found, the current World is returned.
func (w *World) PortalDestination(dim Dimension) *World {
	if w.conf.PortalDestination == nil {
		return w
	}
	if res := w.conf.PortalDestination(dim); res != nil {
		return res
	}
	return w
}

// Close closes the World and saves all chunks currently loaded.
func (w *World) Close() error {
	w.o.Do(w.close)
	return nil
}

// close stops the World from ticking, saves all chunks to the Provider and updates the World's settings.
func (w *World) close() {
	// Let user code run anything that needs to be finished before the World is closed.
	w.Handler().HandleClose()
	w.Handle(NopHandler{})

	close(w.closing)
	w.running.Wait()

	w.conf.Log.Debugf("Saving chunks in memory to disk...")

	w.Query(func(txn *Tx) {
		for pos, c := range w.chunks {
			w.saveChunk(pos, c)
		}
		maps.Clear(w.chunks)
	})
	close(w.q)

	w.set.ref.Dec()
	if !w.advance {
		return
	}

	if !w.conf.ReadOnly {
		w.conf.Log.Debugf("Updating level.dat values...")

		w.provider().SaveSettings(w.set)
	}

	w.conf.Log.Debugf("Closing provider...")
	if err := w.provider().Close(); err != nil {
		w.conf.Log.Errorf("error closing World provider: %v", err)
	}
}

// allViewers returns a list of all loaders of the World, regardless of where in the World they are viewing.
func (w *World) allViewers() ([]Viewer, []*Loader) {
	w.viewersMu.Lock()
	defer w.viewersMu.Unlock()

	viewers, loaders := make([]Viewer, 0, len(w.viewers)), make([]*Loader, 0, len(w.viewers))
	for k, v := range w.viewers {
		viewers = append(viewers, v)
		loaders = append(loaders, k)
	}
	return viewers, loaders
}

// addWorldViewer adds a viewer to the World. Should only be used while the viewer isn't viewing any chunks.
func (w *World) addWorldViewer(l *Loader) {
	w.viewersMu.Lock()
	w.viewers[l] = l.viewer
	w.viewersMu.Unlock()
	l.viewer.ViewTime(w.Time())
	w.set.Lock()
	raining, thundering := w.set.Raining, w.set.Raining && w.set.Thundering
	w.set.Unlock()
	l.viewer.ViewWeather(raining, thundering)
	l.viewer.ViewWorldSpawn(w.Spawn())
}

// removeWorldViewer removes a viewer from the World. Should only be used while the viewer isn't viewing any chunks.
func (w *World) removeWorldViewer(l *Loader) {
	w.viewersMu.Lock()
	delete(w.viewers, l)
	w.viewersMu.Unlock()
}

// addViewer adds a viewer to the World at a given position. Any events that happen in the chunk at that
// position, such as block changes, entity changes etc., will be sent to the viewer.
func (w *World) addViewer(c *chunkData, loader *Loader) {
	c.v = append(c.v, loader.viewer)
	c.l = append(c.l, loader)

	for _, entity := range c.entities {
		showEntity(entity, loader.viewer)
	}
}

// removeViewer removes a viewer from the World at a given position. All entities will be hidden from the
// viewer and no more calls will be made when events in the chunk happen.
func (w *World) removeViewer(pos ChunkPos, loader *Loader) {
	c, ok := w.chunks[pos]
	if !ok {
		return
	}
	if i := slices.Index(c.l, loader); i != -1 {
		c.v = slices.Delete(c.v, i, i+1)
		c.l = slices.Delete(c.l, i, i+1)
	}
	// After removing the loader from the chunk, we also need to hide all entities from the viewer.
	for _, entity := range c.entities {
		loader.viewer.HideEntity(entity)
	}
}

// provider returns the provider of the World. It should always be used, rather than direct field access, in
// order to provide synchronisation safety.
func (w *World) provider() Provider {
	return w.conf.Provider
}

// Handler returns the Handler of the World. It should always be used, rather than direct field access, in
// order to provide synchronisation safety.
func (w *World) Handler() Handler {
	return w.handler.Load()
}

// showEntity shows an entity to a viewer of the World. It makes sure everything of the entity, including the
// items held, is shown.
func showEntity(e Entity, viewer Viewer) {
	viewer.ViewEntity(e)
	viewer.ViewEntityItems(e)
	viewer.ViewEntityArmour(e)
}

// chunk reads a chunk from the position passed. If a chunk at that position is not yet loaded, the chunk is
// loaded from the provider, or generated if it did not yet exist. Both of these actions are done
// synchronously.
// An error is returned if the chunk could not be loaded successfully.
// chunk locks the chunk returned, meaning that any call to chunk made at the same time has to wait until the
// user calls Chunk.Unlock() on the chunk returned.
func (w *World) chunk(pos ChunkPos) *chunkData {
	c, ok := w.chunks[pos]
	if ok {
		// Chunk was already loaded.
		return c
	}
	c, err := w.loadChunk(pos)
	chunk.LightArea([]*chunk.Chunk{c.Chunk}, int(pos[0]), int(pos[1])).Fill()
	if err != nil {
		w.conf.Log.Errorf("load chunk: failed loading %v: %v\n", pos, err)
		return c
	}
	w.calculateLight(pos)
	return c
}

// loadChunk attempts to load a chunk from the provider, or generates a chunk if one doesn't currently exist.
func (w *World) loadChunk(pos ChunkPos) (*chunkData, error) {
	c, found, err := w.provider().LoadChunk(pos, w.conf.Dim)
	if err != nil {
		return newChunkData(chunk.New(airRID, w.ra)), err
	}

	if !found {
		// The provider doesn't have a chunk saved at this position, so we generate a new one.
		data := newChunkData(chunk.New(airRID, w.ra))
		w.chunks[pos] = data
		w.conf.Generator.GenerateChunk(pos, data.Chunk)
		return data, nil
	}
	data := newChunkData(c)
	w.chunks[pos] = data

	ent, err := w.provider().LoadEntities(pos, w.conf.Dim, w.conf.Entities)
	if err != nil {
		return nil, fmt.Errorf("error loading entities of chunk %v: %w", pos, err)
	}
	data.entities = make([]Entity, 0, len(ent))

	for _, e := range ent {
		data.entities = append(data.entities, e)
		w.entities[e] = pos
	}

	blockEntities, err := w.provider().LoadBlockNBT(pos, w.conf.Dim)
	if err != nil {
		return nil, fmt.Errorf("error loading block entities of chunk %v: %w", pos, err)
	}
	w.loadIntoBlocks(data, blockEntities)
	return data, nil
}

// calculateLight calculates the light in the chunk passed and spreads the light of any of the surrounding
// neighbours if they have all chunks loaded around it as a result of the one passed.
func (w *World) calculateLight(centre ChunkPos) {
	for x := int32(-1); x <= 1; x++ {
		for z := int32(-1); z <= 1; z++ {
			// For all the neighbours of this chunk, if they exist, check if all neighbours of that chunk
			// now exist because of this one.
			pos := ChunkPos{centre[0] + x, centre[1] + z}
			if _, ok := w.chunks[pos]; ok {
				// Attempt to spread the light of all neighbours into the ones surrounding them.
				w.spreadLight(pos)
			}
		}
	}
}

// spreadLight spreads the light from the chunk passed at the position passed to all neighbours if each of
// them is loaded.
func (w *World) spreadLight(pos ChunkPos) {
	chunks := make([]*chunk.Chunk, 0, 9)
	for z := int32(-1); z <= 1; z++ {
		for x := int32(-1); x <= 1; x++ {
			neighbour, ok := w.chunks[ChunkPos{pos[0] + x, pos[1] + z}]
			if !ok {
				// Not all surrounding chunks existed: Stop spreading light as we can't do it completely yet.
				return
			}
			chunks = append(chunks, neighbour.Chunk)
		}
	}
	// All chunks of the current one are present, so we can spread the light from this chunk
	// to all chunks.
	chunk.LightArea(chunks, int(pos[0])-1, int(pos[1])-1).Spread()
}

// loadIntoBlocks loads the block entity data passed into blocks located in a specific chunk. The blocks that
// have NBT will then be stored into memory.
func (w *World) loadIntoBlocks(c *chunkData, blockEntityData []map[string]any) {
	c.e = make(map[cube.Pos]Block, len(blockEntityData))
	for _, data := range blockEntityData {
		pos := blockPosFromNBT(data)

		id := c.Block(uint8(pos[0]), int16(pos[1]), uint8(pos[2]), 0)
		b, ok := BlockByRuntimeID(id)
		if !ok {
			w.conf.Log.Errorf("error loading block entity data: could not find block state by runtime ID %v", id)
			continue
		}
		if nbt, ok := b.(NBTer); ok {
			b = nbt.DecodeNBT(data).(Block)
		}
		c.e[pos] = b
	}
}

// saveChunk is called when a chunk is removed from the cache. We first compact the chunk, then we write it to
// the provider.
func (w *World) saveChunk(pos ChunkPos, c *chunkData) {
	if !w.conf.ReadOnly {
		if len(c.e) > 0 || c.m {
			c.Compact()
			if err := w.provider().SaveChunk(pos, c.Chunk, w.conf.Dim); err != nil {
				w.conf.Log.Errorf("error saving chunk %v to provider: %v", pos, err)
			}

			m := make([]map[string]any, 0, len(c.e))
			for pos, b := range c.e {
				if n, ok := b.(NBTer); ok {
					data := n.EncodeNBT()
					data["x"], data["y"], data["z"] = int32(pos[0]), int32(pos[1]), int32(pos[2])
					m = append(m, data)
				}
			}
			if err := w.provider().SaveBlockNBT(pos, m, w.conf.Dim); err != nil {
				w.conf.Log.Errorf("error saving block NBT in chunk %v to provider: %v", pos, err)
			}
		}

		s := make([]Entity, 0, len(c.entities))
		for _, e := range c.entities {
			if _, ok := e.Type().(SaveableEntityType); ok {
				s = append(s, e)
			}
		}
		if err := w.provider().SaveEntities(pos, s, w.conf.Dim); err != nil {
			w.conf.Log.Errorf("error saving entities in chunk %v to provider: %v", pos, err)
		}
	}
	for _, e := range c.entities {
		_ = e.Close()
	}
	c.entities = nil
}

// chunkCacheJanitor runs until the World is running, cleaning chunks that are no longer in use from the cache.
func (w *World) chunkCacheJanitor() {
	t := time.NewTicker(time.Minute * 5)
	defer t.Stop()

	w.running.Add(1)
	for {
		select {
		case <-t.C:
			w.Query(saveUnusedChunks)
		case <-w.closing:
			w.running.Done()
			return
		}
	}
}

func saveUnusedChunks(tx *Tx) {
	w := tx.World()
	for pos, c := range w.chunks {
		if len(c.v) == 0 {
			w.saveChunk(pos, c)
			delete(w.chunks, pos)
		}
	}
}

// chunkData represents the data of a chunk including the block entities and loaders. This data is protected
// by the mutex present in the chunk.Chunk held.
type chunkData struct {
	*chunk.Chunk
	m        bool
	e        map[cube.Pos]Block
	v        []Viewer
	l        []*Loader
	entities []Entity
}

// BlockEntities returns the block entities of the chunk.
func (c *chunkData) BlockEntities() map[cube.Pos]Block {
	return maps.Clone(c.e)
}

// Viewers returns the viewers of the chunk.
func (c *chunkData) Viewers() []Viewer {
	return slices.Clone(c.v)
}

// Entities returns the entities of the chunk.
func (c *chunkData) Entities() []Entity {
	return slices.Clone(c.entities)
}

// newChunkData returns a new chunkData wrapper around the chunk.Chunk passed.
func newChunkData(c *chunk.Chunk) *chunkData {
	return &chunkData{Chunk: c, e: map[cube.Pos]Block{}}
}
