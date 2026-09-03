// Package cmd — client factory seam + JSON / storage helpers.
//
// This file inlines the small set of helpers every leaf command
// needs (open a Client, detect --json, resolve storage path,
// read/write the active-notebook pointer). The helpers used to
// live in internal/cli; they moved here so the cmd package can
// be imported by internal/cli without creating an import cycle.
package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/raihankhan/notebooklm-go/internal/config"
	"github.com/raihankhan/notebooklm-go/internal/paths"
	"github.com/raihankhan/notebooklm-go/notebooklm"
)

// flagStorage is the --storage persistent flag name. Mirrors
// flagStorage; duplicated here so this package does not
// import internal/cli.
const flagStorage = "storage"

// clientFactory builds a *notebooklm.Client from a cobra.Command
// (so it can read the persistent flags) and a context.
type clientFactory func(cmd *cobra.Command, ctx context.Context) (*notebooklm.Client, error)

// defaultFactory is the production ClientFactory. It honors
// --storage from cmd's flag set and resolves the storage path
// through internal/config + internal/paths.
func defaultFactory(cmd *cobra.Command, ctx context.Context) (*notebooklm.Client, error) {
	if cmd == nil {
		return nil, errors.New("cmd: nil cobra.Command")
	}
	if ctx == nil {
		return nil, errors.New("cmd: nil context")
	}
	storageFlag, _ := cmd.Flags().GetString(flagStorage)
	backendFlag, _ := cmd.Flags().GetString("backend")

	var storagePath string
	switch {
	case storageFlag != "":
		storagePath = storageFlag
	default:
		cfg, err := config.Resolve()
		if err != nil {
			return nil, err
		}
		storagePath, err = paths.StoragePath(cfg.Profile)
		if err != nil {
			return nil, err
		}
	}

	return notebooklm.New(ctx,
		notebooklm.WithStoragePath(storagePath),
		notebooklm.WithBackend(notebooklm.BackendName(backendFlag)),
	)
}

// currentFactory is the factory every command uses. Tests swap
// it via SetFactory; production code never reassigns it.
var currentFactory clientFactory = defaultFactory //nolint:gochecknoglobals // factory registry

// SetFactory installs a new ClientFactory and returns a function
// that restores the previous one. Tests should defer the returned
// restore so a test failure does not leak the fake factory into
// sibling tests in the same package. Passing nil restores the
// default production factory.
func SetFactory(f clientFactory) func() {
	prev := currentFactory
	if f == nil {
		currentFactory = defaultFactory
	} else {
		currentFactory = f
	}
	return func() { currentFactory = prev }
}

// newClient is the canonical entry point every leaf command uses
// to obtain an opened *notebooklm.Client. The caller owns the
// returned client and must Close it (the standard SDK contract).
func newClient(cmd *cobra.Command, ctx context.Context) (*notebooklm.Client, error) {
	return currentFactory(cmd, ctx)
}

// resolveStoragePath returns the absolute storage_state.json path
// for the active profile, honoring --storage when set.
func resolveStoragePath(storageOverride string) (string, error) {
	if storageOverride != "" {
		return storageOverride, nil
	}
	cfg, err := config.Resolve()
	if err != nil {
		return "", err
	}
	return paths.StoragePath(cfg.Profile)
}

// jsonRequested reports whether --json was set on cmd's flag set
// or via NOTEBOOKLM_OUTPUT=json. Mirrors internal/cli.JSONRequested;
// duplicated here so the cmd package does not import internal/cli.
func jsonRequested(cmd *cobra.Command) bool {
	if f := cmd.Flags().Lookup("json"); f != nil {
		if f.Value.Type() == "bool" {
			v, _ := cmd.Flags().GetBool("json")
			if v {
				return true
			}
		}
	}
	if v := os.Getenv("NOTEBOOKLM_OUTPUT"); v == "json" {
		return true
	}
	return false
}

// contextDoc is the on-disk shape of context.json.
type contextDoc struct {
	NotebookID string `json:"notebook_id"`
	Title      string `json:"title,omitempty"`
	Role       string `json:"role,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"`
	IsOwner    *bool  `json:"is_owner,omitempty"`
}

const contextFilename = "context.json"

// errContextNotFound is returned by getActiveNotebook when no
// context file exists at the resolved path.
var errContextNotFound = errors.New("cmd: no active notebook context")

// resolveContextPath returns the absolute context.json path.
func resolveContextPath(storageOverride string) (string, error) {
	if storageOverride != "" {
		return filepath.Join(filepath.Dir(storageOverride), contextFilename), nil
	}
	cfg, err := config.Resolve()
	if err != nil {
		return "", err
	}
	profileDir, err := paths.ProfileDir(cfg.Profile)
	if err != nil {
		return "", err
	}
	return filepath.Join(profileDir, contextFilename), nil
}

// getActiveNotebook reads the active-notebook pointer.
func getActiveNotebook(storageOverride string) (contextDoc, error) {
	path, err := resolveContextPath(storageOverride)
	if err != nil {
		return contextDoc{}, err
	}
	data, err := os.ReadFile(path) // #nosec G304 -- operator-controlled profile dir.
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return contextDoc{}, errContextNotFound
		}
		return contextDoc{}, fmt.Errorf("cmd: read context %s: %w", path, err)
	}
	var doc contextDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return contextDoc{}, fmt.Errorf("cmd: parse context %s: %w", path, err)
	}
	return doc, nil
}

// setActiveNotebook persists the supplied context document.
func setActiveNotebook(storageOverride string, doc contextDoc) (string, error) {
	path, err := resolveContextPath(storageOverride)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("cmd: mkdir context dir: %w", err)
	}
	bytes, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", fmt.Errorf("cmd: marshal context: %w", err)
	}
	if err := atomicWriteCmd(path, bytes, 0o600); err != nil {
		return "", fmt.Errorf("cmd: write context: %w", err)
	}
	return path, nil
}

// clearActiveNotebook removes the active-notebook pointer.
func clearActiveNotebook(storageOverride string) error {
	path, err := resolveContextPath(storageOverride)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("cmd: remove context %s: %w", path, err)
}

// atomicWriteCmd performs a temp + chmod + rename write so a
// crash mid-write can never leave a half-written context.json
// on disk.
func atomicWriteCmd(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".context-*.json.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
