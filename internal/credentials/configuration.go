package credentials

import (
	"context"
	"database/sql"
	"errors"
)

// Configuration contains routing metadata only, never credential material.
type Configuration struct {
	Nodes       []Node
	Connections []Connection
}

func (c Configuration) Clone() Configuration {
	copy := Configuration{Nodes: append([]Node(nil), c.Nodes...), Connections: append([]Connection(nil), c.Connections...)}
	for i := range copy.Nodes {
		copy.Nodes[i].Transports = append([]string(nil), copy.Nodes[i].Transports...)
	}
	for i := range copy.Connections {
		copy.Connections[i].Secret = Secret{}
	}
	return copy
}

// Publisher compiles a candidate before the supplied durable transaction runs.
// Install it before serving; standalone stores retain their transaction behavior.
type Publisher func(context.Context, func(*Configuration) error, func(*sql.Tx) error) error

var errNoChange = errors.New("provider configuration unchanged")

func (s *Store) SetPublisher(publish Publisher) { s.publish = publish }

func (s *Store) update(ctx context.Context, change func(*Configuration) error, persist func(*sql.Tx) error) error {
	if s.publish != nil {
		err := s.publish(ctx, change, persist)
		if errors.Is(err, errNoChange) {
			return nil
		}
		return err
	}
	config, err := LoadConfiguration(ctx, s.db)
	if err != nil {
		return err
	}
	if err := change(&config); err != nil {
		if errors.Is(err, errNoChange) {
			return nil
		}
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := persist(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) exec(ctx context.Context, change func(*Configuration) error, query string, args ...any) error {
	return s.update(ctx, change, func(tx *sql.Tx) error {
		return requireAffected(tx.ExecContext(ctx, query, args...))
	})
}

func requireAffected(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotFound
	}
	return nil
}

func editConnection(id string, edit func(*Connection)) func(*Configuration) error {
	return func(c *Configuration) error {
		for i := range c.Connections {
			if c.Connections[i].ID == id {
				edit(&c.Connections[i])
				return nil
			}
		}
		return ErrNotFound
	}
}

// LoadConfiguration reads metadata at startup; serving never calls it.
func LoadConfiguration(ctx context.Context, db *sql.DB) (Configuration, error) {
	s := &Store{db: db}
	nodes, err := s.ListNodes(ctx)
	if err != nil {
		return Configuration{}, err
	}
	rows, err := db.QueryContext(ctx, `SELECT c.id,c.node_id,n.provider_id,c.name,c.auth_kind,c.identity,c.enabled,c.priority,COALESCE(c.proxy_pool_id,'') FROM provider_connections c JOIN provider_nodes n ON n.id=c.node_id ORDER BY c.priority,c.name,c.id`)
	if err != nil {
		return Configuration{}, err
	}
	defer rows.Close()
	config := Configuration{Nodes: nodes}
	for rows.Next() {
		var c Connection
		if err := rows.Scan(&c.ID, &c.NodeID, &c.ProviderID, &c.Name, &c.AuthKind, &c.Identity, &c.Enabled, &c.Priority, &c.ProxyPoolID); err != nil {
			return Configuration{}, err
		}
		config.Connections = append(config.Connections, c)
	}
	return config, rows.Err()
}
