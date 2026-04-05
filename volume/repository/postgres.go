// Package repository provides PostgreSQL and in-memory implementations of
// volume.DatabaseDependency.
package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/cire-ly/block-storage-api/storage"
	"github.com/cire-ly/block-storage-api/volume"
)

// PostgresRepository implements volume.DatabaseDependency backed by PostgreSQL.
// Only instantiated in cmd/api/setup.go — never imported by application.go.
type PostgresRepository struct {
	pool *pgxpool.Pool
}

// NewPostgresRepository wraps a connection pool into a PostgresRepository.
func NewPostgresRepository(pool *pgxpool.Pool) *PostgresRepository {
	return &PostgresRepository{pool: pool}
}

func (r *PostgresRepository) SaveVolume(ctx context.Context, v *storage.Volume) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO volumes (id, name, size_mb, state, backend, node_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`, v.ID, v.Name, v.SizeMB, v.State, v.Backend, v.NodeID, v.CreatedAt, v.UpdatedAt)
	if err != nil {
		return fmt.Errorf("save volume: %w", err)
	}
	return nil
}

func (r *PostgresRepository) UpdateVolume(ctx context.Context, v *storage.Volume) error {
	v.UpdatedAt = time.Now().UTC()
	_, err := r.pool.Exec(ctx, `
		UPDATE volumes SET state = $1, node_id = $2, updated_at = $3 WHERE id = $4
	`, v.State, v.NodeID, v.UpdatedAt, v.ID)
	if err != nil {
		return fmt.Errorf("update volume: %w", err)
	}
	return nil
}

// LoadVolume returns nil, nil when no volume with the given name exists.
func (r *PostgresRepository) LoadVolume(ctx context.Context, name string) (*storage.Volume, error) {
	v := &storage.Volume{}
	var nodeID sql.NullString
	err := r.pool.QueryRow(ctx, `
		SELECT id, name, size_mb, state, backend, node_id, created_at, updated_at
		FROM volumes WHERE name = $1
	`, name).Scan(&v.ID, &v.Name, &v.SizeMB, &v.State, &v.Backend, &nodeID, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("load volume: %w", err)
	}
	v.NodeID = nodeID.String
	return v, nil
}

func (r *PostgresRepository) ListVolumes(ctx context.Context) ([]*storage.Volume, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, name, size_mb, state, backend, node_id, created_at, updated_at
		FROM volumes ORDER BY created_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("list volumes: %w", err)
	}
	defer rows.Close()

	return scanVolumes(rows)
}

func (r *PostgresRepository) ListVolumesByState(ctx context.Context, states ...string) ([]*storage.Volume, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, name, size_mb, state, backend, node_id, created_at, updated_at
		FROM volumes WHERE state = ANY($1) ORDER BY created_at DESC
	`, states)
	if err != nil {
		return nil, fmt.Errorf("list volumes by state: %w", err)
	}
	defer rows.Close()

	return scanVolumes(rows)
}

func (r *PostgresRepository) SaveEvent(ctx context.Context, e volume.VolumeEvent) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO volume_events (volume_id, event, from_state, to_state, created_at)
		VALUES ($1, $2, $3, $4, $5)
	`, e.VolumeID, e.Event, e.FromState, e.ToState, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("save event: %w", err)
	}
	return nil
}

// scanVolumes reads all rows into a slice of Volume pointers.
func scanVolumes(rows pgx.Rows) ([]*storage.Volume, error) {
	var volumes []*storage.Volume
	for rows.Next() {
		v := &storage.Volume{}
		var nodeID sql.NullString
		if err := rows.Scan(
			&v.ID, &v.Name, &v.SizeMB, &v.State, &v.Backend,
			&nodeID, &v.CreatedAt, &v.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan volume: %w", err)
		}
		v.NodeID = nodeID.String
		volumes = append(volumes, v)
	}
	return volumes, rows.Err()
}
