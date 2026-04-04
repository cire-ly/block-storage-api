package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cire-ly/block-storage-api/storage"
)

// DB wraps a pgxpool for volume CRUD and event audit trail.
type DB struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *DB {
	return &DB{pool: pool}
}

// Connect opens a pgxpool and verifies connectivity.
func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("pgxpool.New: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db ping: %w", err)
	}
	return pool, nil
}

func (d *DB) InsertVolume(ctx context.Context, v *storage.Volume) error {
	_, err := d.pool.Exec(ctx, `
		INSERT INTO volumes (id, name, size_mb, state, backend, node_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, v.ID, v.Name, v.SizeMB, v.State, v.Backend, v.NodeID, v.CreatedAt, v.UpdatedAt)
	if err != nil {
		return fmt.Errorf("db insert volume: %w", err)
	}
	return nil
}

func (d *DB) GetVolume(ctx context.Context, id string) (*storage.Volume, error) {
	v := &storage.Volume{}
	var nodeID sql.NullString
	err := d.pool.QueryRow(ctx, `
		SELECT id, name, size_mb, state, backend, node_id, created_at, updated_at
		FROM volumes WHERE id = $1
	`, id).Scan(&v.ID, &v.Name, &v.SizeMB, &v.State, &v.Backend, &nodeID, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("db get volume: %w", err)
	}
	v.NodeID = nodeID.String
	return v, nil
}

func (d *DB) GetVolumeByName(ctx context.Context, name string) (*storage.Volume, error) {
	v := &storage.Volume{}
	var nodeID sql.NullString
	err := d.pool.QueryRow(ctx, `
		SELECT id, name, size_mb, state, backend, node_id, created_at, updated_at
		FROM volumes WHERE name = $1
	`, name).Scan(&v.ID, &v.Name, &v.SizeMB, &v.State, &v.Backend, &nodeID, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("db get volume by name: %w", err)
	}
	v.NodeID = nodeID.String
	return v, nil
}

func (d *DB) ListVolumes(ctx context.Context) ([]*storage.Volume, error) {
	rows, err := d.pool.Query(ctx, `
		SELECT id, name, size_mb, state, backend, node_id, created_at, updated_at
		FROM volumes ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("db list volumes: %w", err)
	}
	defer rows.Close()

	var volumes []*storage.Volume
	for rows.Next() {
		v := &storage.Volume{}
		var nodeID sql.NullString
		if err := rows.Scan(&v.ID, &v.Name, &v.SizeMB, &v.State, &v.Backend, &nodeID, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, fmt.Errorf("db scan volume: %w", err)
		}
		v.NodeID = nodeID.String
		volumes = append(volumes, v)
	}
	return volumes, rows.Err()
}

func (d *DB) UpdateVolumeState(ctx context.Context, id, state, nodeID string) error {
	_, err := d.pool.Exec(ctx, `
		UPDATE volumes SET state = $1, node_id = $2, updated_at = $3 WHERE id = $4
	`, state, nodeID, time.Now().UTC(), id)
	if err != nil {
		return fmt.Errorf("db update volume state: %w", err)
	}
	return nil
}

func (d *DB) DeleteVolume(ctx context.Context, id string) error {
	_, err := d.pool.Exec(ctx, `DELETE FROM volumes WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("db delete volume: %w", err)
	}
	return nil
}

// InsertVolumeEvent appends an FSM transition to the audit trail.
func (d *DB) InsertVolumeEvent(ctx context.Context, volumeID, event, fromState, toState string) error {
	_, err := d.pool.Exec(ctx, `
		INSERT INTO volume_events (volume_id, event, from_state, to_state, created_at)
		VALUES ($1, $2, $3, $4, $5)
	`, volumeID, event, fromState, toState, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("db insert volume event: %w", err)
	}
	return nil
}
