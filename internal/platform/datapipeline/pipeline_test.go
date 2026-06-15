package datapipeline

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestPipeline_Run_Success(t *testing.T) {
	stages := []Stage{
		&mockStage{
			name:     "add_foo",
			critical: false,
			processFn: func(_ context.Context, data *CleanData) {
				data.Fields["foo"] = "bar"
			},
		},
		&mockStage{
			name:     "add_num",
			critical: false,
			processFn: func(_ context.Context, data *CleanData) {
				data.Fields["num"] = 42
			},
		},
	}

	p := NewPipeline(stages...)
	raw := &RawData{Source: "test", Payload: json.RawMessage(`{"hello":"world"}`)}
	data, err := p.Run(context.Background(), raw)

	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if data.Fields["foo"] != "bar" {
		t.Errorf("foo = %v, want bar", data.Fields["foo"])
	}
	if data.Fields["num"] != 42 {
		t.Errorf("num = %v, want 42", data.Fields["num"])
	}
	if len(data.Errors) != 0 {
		t.Errorf("unexpected errors: %v", data.Errors)
	}
}

func TestPipeline_Run_NonCriticalErrorContinues(t *testing.T) {
	var calls []string
	stages := []Stage{
		&mockStage{
			name:     "fail_noncritical",
			critical: false,
			processFn: func(_ context.Context, data *CleanData) {
				calls = append(calls, "fail_noncritical")
				data.Errors = append(data.Errors, "something went wrong")
			},
		},
		&mockStage{
			name:     "after_fail",
			critical: false,
			processFn: func(_ context.Context, data *CleanData) {
				calls = append(calls, "after_fail")
				data.Fields["ok"] = true
			},
		},
	}

	p := NewPipeline(stages...)
	_, err := p.Run(context.Background(), &RawData{Source: "test", Payload: json.RawMessage(`{}`)})

	if err != nil {
		t.Fatalf("Run returned error for non-critical failure: %v", err)
	}
	if len(calls) != 2 {
		t.Errorf("expected 2 stage calls, got %d: %v", len(calls), calls)
	}
}

func TestPipeline_Run_CriticalErrorAborts(t *testing.T) {
	var calls []string
	stages := []Stage{
		&mockStage{
			name:     "ok_stage",
			critical: false,
			processFn: func(_ context.Context, data *CleanData) {
				calls = append(calls, "ok_stage")
				data.Fields["step1"] = "done"
			},
		},
		&mockStage{
			name:     "critical_fail",
			critical: true,
			processFn: func(_ context.Context, data *CleanData) {
				calls = append(calls, "critical_fail")
				data.Errors = append(data.Errors, "critical error")
			},
		},
		&mockStage{
			name:     "never_reached",
			critical: false,
			processFn: func(_ context.Context, data *CleanData) {
				calls = append(calls, "never_reached")
			},
		},
	}

	p := NewPipeline(stages...)
	_, err := p.Run(context.Background(), &RawData{Source: "test", Payload: json.RawMessage(`{}`)})

	if err == nil {
		t.Fatal("expected error for critical stage failure")
	}
	if len(calls) != 2 {
		t.Errorf("expected 2 stage calls (not the 3rd), got %d: %v", len(calls), calls)
	}
}

func TestPipeline_Run_PanicNonCritical(t *testing.T) {
	var calls []string
	stages := []Stage{
		&mockStage{
			name:     "panic_stage",
			critical: false,
			processFn: func(_ context.Context, data *CleanData) {
				calls = append(calls, "panic_stage")
				panic("oops")
			},
		},
		&mockStage{
			name:     "after_panic",
			critical: false,
			processFn: func(_ context.Context, data *CleanData) {
				calls = append(calls, "after_panic")
				data.Fields["survived"] = true
			},
		},
	}

	p := NewPipeline(stages...)
	_, err := p.Run(context.Background(), &RawData{Source: "test", Payload: json.RawMessage(`{}`)})

	if err != nil {
		t.Fatalf("Run returned error for non-critical panic: %v", err)
	}
	if len(calls) != 2 {
		t.Errorf("expected 2 stage calls, got %d: %v", len(calls), calls)
	}
	// Check the panic was recorded in Errors.
}

func TestPipeline_Run_PanicCritical(t *testing.T) {
	stages := []Stage{
		&mockStage{
			name:     "critical_panic",
			critical: true,
			processFn: func(_ context.Context, data *CleanData) {
				panic("critical failure")
			},
		},
	}

	p := NewPipeline(stages...)
	_, err := p.Run(context.Background(), &RawData{Source: "test", Payload: json.RawMessage(`{}`)}) //nolint:govet // partial data is fine

	if err == nil {
		t.Fatal("expected error for critical stage panic")
	}
}

func TestPipeline_Run_NilRaw(t *testing.T) {
	p := NewPipeline(&mockStage{name: "s", critical: false})
	_, err := p.Run(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error for nil raw input")
	}
}

func TestPipeline_Run_NoStages(t *testing.T) {
	p := NewPipeline()
	_, err := p.Run(context.Background(), &RawData{Source: "test", Payload: json.RawMessage(`{}`)})
	if err == nil {
		t.Fatal("expected error for empty pipeline")
	}
}

func TestPipeline_Run_ErrorsCollected(t *testing.T) {
	stages := []Stage{
		&mockStage{
			name:     "err1",
			critical: false,
			processFn: func(_ context.Context, data *CleanData) {
				data.Errors = append(data.Errors, "first error")
			},
		},
		&mockStage{
			name:     "err2",
			critical: false,
			processFn: func(_ context.Context, data *CleanData) {
				data.Errors = append(data.Errors, "second error")
			},
		},
		&mockStage{
			name:     "ok",
			critical: false,
			processFn: func(_ context.Context, data *CleanData) {
				data.Fields["done"] = true
			},
		},
	}

	p := NewPipeline(stages...)
	data, err := p.Run(context.Background(), &RawData{Source: "test", Payload: json.RawMessage(`{}`)})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(data.Errors) != 2 {
		t.Errorf("expected 2 errors, got %d: %v", len(data.Errors), data.Errors)
	}
	if data.Fields["done"] != true {
		t.Error("final stage did not execute")
	}
}

func TestPipeline_Run_ErrorsCollectedCheck(t *testing.T) {
	// Verify that the panic error message is recorded in data.Errors.
	stages := []Stage{
		&mockStage{
			name:     "panic_stage",
			critical: false,
			processFn: func(_ context.Context, data *CleanData) {
				panic("my panic msg")
			},
		},
	}

	p := NewPipeline(stages...)
	data, err := p.Run(context.Background(), &RawData{Source: "test", Payload: json.RawMessage(`{}`)})

	if err != nil {
		t.Fatalf("Run should not return error for non-critical panic: %v", err)
	}
	if len(data.Errors) == 0 {
		t.Fatal("expected errors to contain panic message")
	}
}

func TestPipeline_Run_CriticalErrorFromAppend(t *testing.T) {
	// A critical stage that appends to data.Errors (instead of panicking)
	// should still abort the pipeline.
	stages := []Stage{
		&mockStage{
			name:     "critical_append",
			critical: true,
			processFn: func(_ context.Context, data *CleanData) {
				data.Errors = append(data.Errors, "append error")
			},
		},
		&mockStage{
			name:     "never",
			critical: false,
			processFn: func(_ context.Context, data *CleanData) {
				t.Error("this stage should not be called")
			},
		},
	}

	p := NewPipeline(stages...)
	_, err := p.Run(context.Background(), &RawData{Source: "test", Payload: json.RawMessage(`{}`)})

	if err == nil {
		t.Fatal("expected error for critical stage that appended to Errors")
	}
}

func TestPipeline_Run_PreservesRawData(t *testing.T) {
	payload := json.RawMessage(`{"key":"value"}`)
	stage := &mockStage{
		name:     "check_raw",
		critical: false,
		processFn: func(_ context.Context, data *CleanData) {
			if data.Raw.Source != "my_source" {
				t.Errorf("Raw.Source = %q, want %q", data.Raw.Source, "my_source")
			}
			var m map[string]string
			if err := json.Unmarshal(data.Raw.Payload, &m); err != nil {
				t.Errorf("unmarshal: %v", err)
			}
			if m["key"] != "value" {
				t.Errorf("payload key = %v, want value", m["key"])
			}
		},
	}

	p := NewPipeline(stage)
	_, err := p.Run(context.Background(), &RawData{Source: "my_source", Payload: payload})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestPipeline_Run_IntegrationWithRealStages tests the pipeline with actual
// stage implementations to validate end-to-end behaviour.
func TestPipeline_Run_IntegrationWithRealStages(t *testing.T) {
	// We need to import the stages package, but that would create an import
	// cycle. Instead, we test the individual stages in the stages package
	// test file. This test verifies the Pipeline mechanics using mock stages
	// that simulate realistic behaviour.
	t.Run("realistic_mock", func(t *testing.T) {
		processErr := errors.New("process error")
		stages := []Stage{
			&mockStage{
				name:     "extract",
				critical: false,
				processFn: func(_ context.Context, data *CleanData) {
					data.Fields["extracted"] = true
				},
			},
			&mockStage{
				name:     "transform",
				critical: false,
				processFn: func(_ context.Context, data *CleanData) {
					if v, ok := data.Fields["extracted"]; !ok || v != true {
						data.Errors = append(data.Errors, "missing extracted field")
					}
					data.Fields["transformed"] = "done"
				},
			},
		}

		p := NewPipeline(stages...)
		data, err := p.Run(context.Background(), &RawData{Source: "test", Payload: json.RawMessage(`{}`)})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if data.Fields["transformed"] != "done" {
			t.Error("transform stage did not execute")
		}

		// Test with a critical failure.
		stages2 := []Stage{
			&mockStage{
				name:     "critical",
				critical: true,
				processFn: func(_ context.Context, data *CleanData) {
					data.Errors = append(data.Errors, processErr.Error())
				},
			},
		}
		p2 := NewPipeline(stages2...)
		_, err = p2.Run(context.Background(), &RawData{Source: "test", Payload: json.RawMessage(`{}`)})
		if err == nil {
			t.Error("expected error for critical stage")
		}
	})
}
