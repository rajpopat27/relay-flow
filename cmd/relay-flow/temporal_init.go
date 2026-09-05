package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/rajpopat27/relay-flow/internal/execution/projection"
	"go.temporal.io/api/serviceerror"
	workflowservice "go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"google.golang.org/protobuf/types/known/durationpb"
)

var ErrProjectionUnusable = errors.New("relay projection is unusable")

const (
	executorGoworkflows      = "goworkflows"
	executorTemporal         = "temporal"
	defaultTemporalAddr      = "localhost:7233"
	minimumTemporalRetention = 30 * 24 * time.Hour
)

// executorSelectField is the exact interactive executor selection required by
// the Temporal MVP. The first option deliberately remains the embedded default.
func executorSelectField(value *string) (huh.Field, error) {
	if value == nil {
		return nil, fmt.Errorf("executor selection requires a destination")
	}
	return huh.NewSelect[string]().
		Title("Select executor").
		Options(huh.NewOptions(executorGoworkflows, executorTemporal)...).
		Value(value), nil
}

func temporalAddressField(value *string) (huh.Field, error) {
	if value == nil {
		return nil, fmt.Errorf("Temporal address requires a destination")
	}
	if *value == "" {
		*value = defaultTemporalAddr
	}
	return huh.NewInput().
		Title("Temporal server address").
		Value(value), nil
}

func temporalNamespaceField(value *string) (huh.Field, error) {
	if value == nil {
		return nil, fmt.Errorf("Temporal namespace requires a destination")
	}
	return huh.NewInput().
		Title("Temporal namespace/team name").
		Value(value), nil
}

func promptTemporalSettings(address, namespace string) (string, string, error) {
	groups := make([]*huh.Group, 0, 2)
	if address == "" {
		field, err := temporalAddressField(&address)
		if err != nil {
			return "", "", err
		}
		groups = append(groups, huh.NewGroup(field))
	}
	if namespace == "" {
		field, err := temporalNamespaceField(&namespace)
		if err != nil {
			return "", "", err
		}
		groups = append(groups, huh.NewGroup(field))
	}
	if len(groups) > 0 {
		if err := huh.NewForm(groups...).Run(); err != nil {
			return "", "", err
		}
	}
	return strings.TrimSpace(address), strings.TrimSpace(namespace), nil
}

func requiredTemporalRetention(days int) time.Duration {
	retention := minimumTemporalRetention
	if days > 0 && time.Duration(days)*24*time.Hour > retention {
		retention = time.Duration(days) * 24 * time.Hour
	}
	return retention
}

// ensureTemporalNamespace creates a missing namespace or verifies an existing
// one through the public SDK. Existing retention is never lowered or silently
// changed by relay-flow.
func verifyExecutorIdentity(path string, expected projection.ExecutorIdentity) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return fmt.Errorf("open executor identity %s: %w", path, err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA schema_version`); err != nil {
		return fmt.Errorf("%w: inspect executor identity %s: %v", ErrProjectionUnusable, path, err)
	}
	// Legacy databases predate the identity table. Migrate only the shared
	// relay projection before verification so an embedded legacy home can be
	// adopted without touching its engine history.
	proj := &projection.RunProjection{DB: db}
	if err := proj.Migrate(); err != nil {
		return fmt.Errorf("%w: migrate relay projection %s: %v", ErrProjectionUnusable, path, err)
	}
	if err := proj.VerifyIdentity(context.Background(), expected); err != nil {
		if errors.Is(err, projection.ErrIdentityMismatch) || errors.Is(err, projection.ErrIdentityMissing) {
			return err
		}
		return fmt.Errorf("%w: verify executor identity %s: %v", ErrProjectionUnusable, path, err)
	}
	return nil
}

func ensureTemporalNamespace(ctx context.Context, address, namespace string, retentionDays int) error {
	address = strings.TrimSpace(address)
	namespace = strings.TrimSpace(namespace)
	if address == "" {
		address = defaultTemporalAddr
	}
	if namespace == "" {
		return fmt.Errorf("Temporal namespace is required")
	}
	if namespace == client.DefaultNamespace {
		return fmt.Errorf("Temporal namespace must be a dedicated named namespace, not %q", client.DefaultNamespace)
	}
	manager, err := client.NewNamespaceClient(client.Options{HostPort: address})
	if err != nil {
		return fmt.Errorf("connect to Temporal namespace manager at %s: %w", address, err)
	}
	defer manager.Close()

	required := requiredTemporalRetention(retentionDays)
	description, err := manager.Describe(ctx, namespace)
	if err != nil {
		var notFound *serviceerror.NamespaceNotFound
		if !errors.As(err, &notFound) {
			return fmt.Errorf("describe Temporal namespace %q: %w", namespace, err)
		}
		if err := manager.Register(ctx, &workflowservice.RegisterNamespaceRequest{
			Namespace:                        namespace,
			Description:                      "relay-flow durable executor",
			WorkflowExecutionRetentionPeriod: durationpb.New(required),
		}); err != nil {
			var alreadyExists *serviceerror.NamespaceAlreadyExists
			if !errors.As(err, &alreadyExists) {
				return fmt.Errorf("register Temporal namespace %q: %w", namespace, err)
			}
		}
		description, err = manager.Describe(ctx, namespace)
		if err != nil {
			return fmt.Errorf("verify registered Temporal namespace %q: %w", namespace, err)
		}
	}
	if description.Config == nil || description.Config.WorkflowExecutionRetentionTtl == nil {
		return fmt.Errorf("Temporal namespace %q has no workflow retention configuration", namespace)
	}
	actual := description.Config.WorkflowExecutionRetentionTtl.AsDuration()
	if actual < required {
		return fmt.Errorf("Temporal namespace %q retention is %s, need at least %s", namespace, actual, required)
	}
	return nil
}
