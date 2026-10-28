package credentials

import (
	"context"
	"errors"
	"testing"
	"time"
)

// ReviseSecret (#299) is Update's exclusion with a credential-only write: an
// Update waits for it, a connection deleted meanwhile is not saved again, a
// refused change writes nothing, and the revision is not counted.
func TestReviseSecretRotatesUnderUpdatesExclusion(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node := seedNode(t, store, "xiaomi-mimo")
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "mimo", AuthKind: AuthAPIKey, Identity: "7@xiaomi", Enabled: true, Secret: Secret{AccessToken: "sk-k", ProviderData: map[string]string{"apiKey": "sk-k", "flag": "on"}}})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(store, nil)
	before, err := store.GetConnection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}

	refused := errors.New("refused")
	if _, err := registry.ReviseSecret(ctx, connection.ID, func(Connection) (Secret, error) { return Secret{}, refused }); !errors.Is(err, refused) {
		t.Fatalf("refused: %v", err)
	}

	// An Update started while change runs waits for the revision.
	inChange, release := make(chan struct{}), make(chan struct{})
	updated := make(chan error, 1)
	revised, err := registry.ReviseSecret(ctx, connection.ID, func(stored Connection) (Secret, error) {
		close(inChange)
		go func() {
			_, err := registry.Update(ctx, connection.ID, func(c Connection) (Connection, error) {
				if c.Secret.ProviderData["mimoPassToken"] != "renewed" {
					t.Error("the update did not see the revision")
				}
				c.Secret.ProviderData["flag"] = "off"
				return c, nil
			})
			updated <- err
		}()
		select {
		case <-updated:
			t.Error("an update ran while change was running")
		case <-time.After(50 * time.Millisecond):
		}
		close(release)
		secret := stored.Secret
		secret.ProviderData = map[string]string{"apiKey": "sk-k", "flag": stored.Secret.ProviderData["flag"], "mimoPassToken": "renewed"}
		return secret, nil
	})
	<-inChange
	<-release
	if err != nil || revised.ProviderData["mimoPassToken"] != "renewed" {
		t.Fatalf("revise: %v %v", revised, err)
	}
	if err := <-updated; err != nil {
		t.Fatal(err)
	}
	after, err := store.GetConnection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Secret.ProviderData["mimoPassToken"] != "renewed" || after.Secret.ProviderData["flag"] != "off" {
		t.Fatalf("stored = %v", after.Secret.ProviderData)
	}
	served, err := registry.Secret(ctx, connection.ID)
	if err != nil || served.ProviderData["flag"] != "off" || served.ProviderData["mimoPassToken"] != "renewed" {
		t.Fatalf("served = %v %v", served.ProviderData, err)
	}
	// The Update above re-saved the same credential, so neither write counted.
	if after.CredentialRevision != before.CredentialRevision {
		t.Fatalf("revision %d -> %d", before.CredentialRevision, after.CredentialRevision)
	}

	// Deleted while change runs: ErrNotFound, and the row stays deleted.
	_, err = registry.ReviseSecret(ctx, connection.ID, func(stored Connection) (Secret, error) {
		if err := store.DeleteConnection(ctx, connection.ID); err != nil {
			t.Fatal(err)
		}
		secret := stored.Secret
		secret.ProviderData = map[string]string{"apiKey": "sk-k", "mimoPassToken": "late"}
		return secret, nil
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted: %v", err)
	}
	if _, err := store.GetConnection(ctx, connection.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the deleted connection is back: %v", err)
	}
}
