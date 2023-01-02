package queued_world

import (
	"fmt"
	"github.com/df-mc/dragonfly/server/block/cube"
	"github.com/df-mc/dragonfly/server/world"
	"github.com/df-mc/dragonfly/server/world/chunk"
	"sync"
)

type World struct {
	conf world.Config
	ra   cube.Range

	once sync.Once
	q    chan QueryFunc

	// columns holds a cache of columns currently loaded. These columns are
	// cleared from this map after some time of not being used.
	columns map[world.ChunkPos]*column
}

func NewWorld() *World {
	w := &World{
		q: make(chan QueryFunc),
	}
	go w.run()
	return w
}

type QueryFunc func(txn *Txn)

func (w *World) Query(f QueryFunc) {
	w.q <- f
}

func (w *World) run() {
	for f := range w.q {
		txn := &Txn{w: w}
		f(txn)
		txn.complete.Store(true)
	}
}

func (w *World) Range() cube.Range {
	return w.ra
}

func (w *World) setBiome(pos cube.Pos, b world.Biome) {
	// ...
}

func (w *World) biome(pos cube.Pos) world.Biome {
	// ...
	return nil
}

func (w *World) setBlock(pos cube.Pos, b world.Block, opts *world.SetOpts) {
	// ...
}

func (w *World) block(pos cube.Pos) world.Block {
	// ...
	return nil
}

func (w *World) chunk(pos world.ChunkPos) *column {
	c, ok := w.columns[pos]
	if ok {
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
func (w *World) loadChunk(pos world.ChunkPos) (*column, error) {
	c, found, err := w.conf.Provider.LoadChunk(pos, w.conf.Dim)
	if err != nil {
		return newColumn(chunk.New(airRID, w.Range())), err
	}

	if !found {
		// The provider doesn't have a chunk saved at this position, so we generate a new one.
		column := newColumn(chunk.New(airRID, w.Range()))
		w.columns[pos] = column
		w.conf.Generator.GenerateChunk(pos, column.Chunk)
		return column, nil
	}
	data := newColumn(c)
	w.columns[pos] = data

	data.entities, err = w.conf.Provider.LoadEntities(pos, w.conf.Dim, w.conf.Entities)
	if err != nil {
		return nil, fmt.Errorf("error loading entities of chunk %v: %w", pos, err)
	}

	blockEntities, err := w.conf.Provider.LoadBlockNBT(pos, w.conf.Dim)
	if err != nil {
		return nil, fmt.Errorf("error loading block entities of chunk %v: %w", pos, err)
	}
	w.loadIntoBlocks(data, blockEntities)

	return data, nil
}

// loadIntoBlocks loads the block entity data passed into blocks located in a specific chunk. The blocks that
// have NBT will then be stored into memory.
func (w *World) loadIntoBlocks(c *column, blockEntityData []map[string]any) {
	c.be = make(map[cube.Pos]world.Block, len(blockEntityData))
	for _, data := range blockEntityData {
		pos := blockPosFromNBT(data)

		id := c.Block(uint8(pos[0]), int16(pos[1]), uint8(pos[2]), 0)
		b, ok := world.BlockByRuntimeID(id)
		if !ok {
			w.conf.Log.Errorf("error loading block entity data: could not find block state by runtime ID %v", id)
			continue
		}
		if nbt, ok := b.(world.NBTer); ok {
			b = nbt.DecodeNBT(data).(world.Block)
		}
		c.be[pos] = b
	}
}

// calculateLight calculates the light in the chunk passed and spreads the light of any of the surrounding
// neighbours if they have all columns loaded around it as a result of the one passed.
func (w *World) calculateLight(centre world.ChunkPos) {
	for x := int32(-1); x <= 1; x++ {
		for z := int32(-1); z <= 1; z++ {
			// For all the neighbours of this chunk, if they exist, check if all neighbours of that chunk
			// now exist because of this one.
			pos := world.ChunkPos{centre[0] + x, centre[1] + z}
			if _, ok := w.columns[pos]; ok {
				// Attempt to spread the light of all neighbours into the ones surrounding them.
				w.spreadLight(pos)
			}
		}
	}
}

// spreadLight spreads the light from the chunk passed at the position passed to all neighbours if each of
// them is loaded.
func (w *World) spreadLight(pos world.ChunkPos) {
	chunks := make([]*chunk.Chunk, 0, 9)
	for z := int32(-1); z <= 1; z++ {
		for x := int32(-1); x <= 1; x++ {
			neighbour, ok := w.columns[world.ChunkPos{pos[0] + x, pos[1] + z}]
			if !ok {
				// Not all surrounding columns existed: Stop spreading light as we can't do it completely yet.
				return
			}
			chunks = append(chunks, neighbour.Chunk)
		}
	}
	// All columns of the current one are present, so we can spread the light from this chunk
	// to all columns.
	chunk.LightArea(chunks, int(pos[0])-1, int(pos[1])-1).Spread()
}

func (w *World) Close() error {
	w.once.Do(func() {
		close(w.q)
	})
	return nil
}

// column represents the data of a chunk including the block entities and loaders. This data is protected
// by the mutex present in the chunk.Chunk held.
type column struct {
	*chunk.Chunk
	m        bool
	be       map[cube.Pos]world.Block
	viewers  []world.Viewer
	loaders  []*world.Loader
	entities []world.Entity
}

// newColumn returns a new column wrapper around the chunk.Chunk passed.
func newColumn(c *chunk.Chunk) *column {
	return &column{Chunk: c, be: map[cube.Pos]world.Block{}}
}
