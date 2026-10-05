package torrents

import (
	"context"
	"errors"
	"sync"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Persist verified pieces in batches so restarts can resume without rehashing completed data.
type pieceStore struct {
	pool  *pgxpool.Pool
	jobID string

	mu     sync.Mutex
	total  int
	known  map[int]bool
	dirty  bool
	closed bool
}

func newPieceStore(pool *pgxpool.Pool, jobID string, total int, bits []byte) *pieceStore {
	store := &pieceStore{pool: pool, jobID: jobID, total: total, known: make(map[int]bool)}
	if total > 0 && len(bits) == (total+7)/8 {
		for index := 0; index < total; index++ {
			if bits[index/8]&(1<<uint(index%8)) != 0 {
				store.known[index] = true
			}
		}
	}
	return store
}

func (p *pieceStore) setTotal(total int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if total >= 0 && total != p.total {
		p.total = total
		p.dirty = true
	}
}

func (p *pieceStore) Get(key metainfo.PieceKey) (storage.Completion, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if complete, ok := p.known[key.Index]; ok {
		return storage.Completion{Ok: true, Complete: complete}, nil
	}
	return storage.Completion{}, nil
}

func (p *pieceStore) Set(key metainfo.PieceKey, complete bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return errors.New("torrents: the resume state is closed")
	}
	if current, ok := p.known[key.Index]; ok && current == complete {
		return nil
	}
	if complete {
		p.known[key.Index] = true
	} else {
		delete(p.known, key.Index)
	}
	p.dirty = true
	return nil
}

func (p *pieceStore) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), persistTimeout)
	defer cancel()
	return p.flush(ctx)
}

func (p *pieceStore) flush(ctx context.Context) error {
	p.mu.Lock()
	if !p.dirty {
		p.mu.Unlock()
		return nil
	}
	done, bits := len(p.known), encodePieces(p.known, p.total)
	p.mu.Unlock()
	if _, err := p.pool.Exec(ctx, `UPDATE torrent_jobs SET pieces_done = $2, piece_bits = $3, updated_at = now()
		WHERE id = $1`, p.jobID, done, bits); err != nil {
		return storeError("save resume state", err)
	}
	p.mu.Lock()
	p.dirty = false
	p.mu.Unlock()
	return nil
}

func (p *pieceStore) completed() (int, []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.known), encodePieces(p.known, p.total)
}

func encodePieces(known map[int]bool, total int) []byte {
	if total <= 0 {
		return []byte{}
	}
	bits := make([]byte, (total+7)/8)
	for index := range known {
		if index >= 0 && index < total {
			bits[index/8] |= 1 << uint(index%8)
		}
	}
	return bits
}
