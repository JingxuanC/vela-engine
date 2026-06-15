package datapipeline

import (
	"context"
	"errors"
	"fmt"
)

// Pipeline composes a sequence of Stage instances and runs them in order.
// The Run method processes a single RawData through all stages, collecting
// errors and optionally aborting on critical-stage failures.
type Pipeline struct {
	stages []Stage
}

// NewPipeline creates a new Pipeline from the given stages. Stages are
// executed in the order they are provided.
func NewPipeline(stages ...Stage) *Pipeline {
	return &Pipeline{stages: stages}
}

// Run executes all stages in order against the provided raw data.
//
// Behaviour:
//   - A new CleanData is initialised with the raw input.
//   - For each stage, Process is called.
//   - If Process panics, it is recovered (the panic value is appended to
//     data.Errors), and if the stage is critical the pipeline aborts.
//   - Non-critical stages that fail (either by panic or by appending errors)
//     do not abort the pipeline — processing continues.
//   - Critical-stage failures cause an immediate error return, along with
//     the partially populated CleanData.
func (p *Pipeline) Run(ctx context.Context, raw *RawData) (data *CleanData, err error) {
	if raw == nil {
		return nil, errors.New("datapipeline: raw input is nil")
	}
	if len(p.stages) == 0 {
		return nil, errors.New("datapipeline: pipeline has no stages")
	}

	data = &CleanData{
		Raw:    *raw,
		Fields: make(map[string]any),
		Errors: nil,
	}

	for _, stage := range p.stages {
		// Check for context cancellation before each stage.
		select {
		case <-ctx.Done():
			return data, ctx.Err()
		default:
		}

		// Track errors before this stage to detect new errors.
		errorsBefore := len(data.Errors)

		var stageErr error
		func() {
			defer func() {
				if r := recover(); r != nil {
					stageErr = fmt.Errorf("panic in stage %q: %v", stage.Name(), r)
				}
			}()
			stage.Process(ctx, data)
		}()

		if stageErr != nil {
			data.Errors = append(data.Errors, stageErr.Error())
			if stage.IsCritical() {
				return data, stageErr
			}
			continue
		}

		// If this critical stage produced new errors (but didn't panic), abort.
		if len(data.Errors) > errorsBefore && stage.IsCritical() {
			errMsg := data.Errors[len(data.Errors)-1]
			return data, fmt.Errorf("datapipeline: critical stage %q failed: %s", stage.Name(), errMsg)
		}
	}

	return data, nil
}
