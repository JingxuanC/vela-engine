// Package datapipeline provides a pluggable data cleaning and normalization pipeline.
//
// Each stage implements the Stage interface and can be composed into a Pipeline
// that processes raw data (e.g. from Shopify webhooks) into a structured CleanData
// result with normalized fields and collected errors.
package datapipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

// CleanData holds the result of pipeline processing: the original raw input,
// accumulated structured fields, and any non-fatal errors encountered during
// processing.
type CleanData struct {
	Raw    RawData
	Fields map[string]any
	Errors []string
}

// RawData wraps a raw source payload, typically from a webhook or API sync.
type RawData struct {
	Source  string
	Payload json.RawMessage
}

// Stage defines a single processing step in a data pipeline.
type Stage interface {
	// Name returns a human-readable name for the stage.
	Name() string

	// Process applies the stage's transformation on data. Implementations
	// should modify data.Fields and append to data.Errors as needed.
	Process(ctx context.Context, data *CleanData)

	// IsCritical indicates whether a failure in this stage should abort
	// the entire pipeline. Non-critical stages log errors to data.Errors
	// and allow the pipeline to continue.
	IsCritical() bool
}

// stageInfo stores a registered stage's factory for the global Registry.
type stageInfo struct {
	Factory func() Stage
	Descr   string
}

// Registry is a global registry of stage constructors. Stages can register
// themselves at init() time, and callers can enumerate or instantiate them
// by name at runtime.
var (
	registry   = map[string]stageInfo{}
	registryMu sync.RWMutex
)

// RegisterStage adds a stage constructor to the global registry. It panics
// if the name is empty or already registered.
func RegisterStage(name, description string, factory func() Stage) {
	registryMu.Lock()
	defer registryMu.Unlock()
	if name == "" {
		panic("datapipeline: RegisterStage requires a non-empty name")
	}
	if _, dup := registry[name]; dup {
		panic(fmt.Sprintf("datapipeline: stage %q already registered", name))
	}
	registry[name] = stageInfo{Factory: factory, Descr: description}
}

// GetStage returns a new instance of the named stage. If not found, ok is
// false.
func GetStage(name string) (Stage, bool) {
	registryMu.RLock()
	info, ok := registry[name]
	registryMu.RUnlock()
	if !ok {
		return nil, false
	}
	return info.Factory(), true
}

// ListStages returns a copy of the current registered stage names and their
// descriptions.
func ListStages() map[string]string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make(map[string]string, len(registry))
	for n, info := range registry {
		out[n] = info.Descr
	}
	return out
}
