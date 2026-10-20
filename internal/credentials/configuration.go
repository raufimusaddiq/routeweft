package credentials

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/raufimusaddiq/routeweft/internal/routing"
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
	config, err := LoadConfiguration(ctx, s.db, s.sealer)
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
func LoadConfiguration(ctx context.Context, db *sql.DB, sealer *Sealer) (Configuration, error) {
	return loadConfiguration(ctx, db, sealer, true)
}

// LoadMetadata omits sealed settings for inventories and structural backup validation only.
func LoadMetadata(ctx context.Context, db *sql.DB) (Configuration, error) {
	return loadConfiguration(ctx, db, nil, false)
}

func (s *Store) Configuration(ctx context.Context) (Configuration, error) {
	return LoadConfiguration(ctx, s.db, s.sealer)
}

func loadConfiguration(ctx context.Context, db *sql.DB, sealer *Sealer, settings bool) (Configuration, error) {
	s := &Store{db: db}
	nodes, err := s.ListNodes(ctx)
	if err != nil {
		return Configuration{}, err
	}
	rows, err := db.QueryContext(ctx, `SELECT c.id,c.node_id,n.provider_id,c.name,c.auth_kind,c.identity,COALESCE(c.secret_blob,''),c.enabled,c.priority,COALESCE(c.proxy_pool_id,''),c.account_id FROM provider_connections c JOIN provider_nodes n ON n.id=c.node_id ORDER BY c.priority,c.name,c.id`)
	if err != nil {
		return Configuration{}, err
	}
	defer rows.Close()
	config := Configuration{Nodes: nodes}
	for rows.Next() {
		var c Connection
		var blob string
		if err := rows.Scan(&c.ID, &c.NodeID, &c.ProviderID, &c.Name, &c.AuthKind, &c.Identity, &blob, &c.Enabled, &c.Priority, &c.ProxyPoolID, &c.AccountID); err != nil {
			return Configuration{}, err
		}
		if blob != "" && settings {
			if sealer == nil {
				return Configuration{}, fmt.Errorf("open provider settings for connection %q: %w", c.ID, ErrKeyRequired)
			}
			payload, err := sealer.Open(blob)
			if err != nil {
				return Configuration{}, fmt.Errorf("open provider settings for connection %q: %w", c.ID, err)
			}
			var secret Secret
			if err := json.Unmarshal(payload, &secret); err != nil {
				return Configuration{}, fmt.Errorf("decode provider settings for connection %q: %w", c.ID, err)
			}
			c.Settings = projectSettings(secret.ProviderData)
		}
		config.Connections = append(config.Connections, c)
	}
	return config, rows.Err()
}

func projectSettings(data map[string]string) routing.ProviderSettings {
	return routing.ProviderSettings{
		BaseURL: data["baseUrl"], AzureEndpoint: data["azureEndpoint"], APIVersion: data["apiVersion"],
		Deployment: data["deployment"], Organization: data["organization"], Region: data["region"],
		WorkspaceID: data["workspaceId"], DeviceID: data["deviceId"], AgentID: data["agentId"],
		Email: data["email"], UserID: data["userId"], SystemID: data["systemId"],
		OrganizationID: data["organizationId"], DefaultOrganizationID: data["defaultOrganizationId"], UID: data["uid"], OrgID: data["orgId"],
	}
}
