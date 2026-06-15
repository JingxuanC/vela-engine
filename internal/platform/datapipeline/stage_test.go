package datapipeline

import (
	"context"
	"testing"
)

// mockStage is a simple Stage implementation for testing Pipeline behaviour.
type mockStage struct {
	name      string
	critical  bool
	processFn func(ctx context.Context, data *CleanData)
}

func (m *mockStage) Name() string     { return m.name }
func (m *mockStage) IsCritical() bool { return m.critical }
func (m *mockStage) Process(ctx context.Context, data *CleanData) {
	if m.processFn != nil {
		m.processFn(ctx, data)
	}
}

func TestRegisterAndGetStage(t *testing.T) {
	// Clean registry state for this test. Since registry is shared across
	// tests, use a unique name.
	name := "test_stage_register"

	RegisterStage(name, "test stage", func() Stage {
		return &mockStage{name: name}
	})

	got, ok := GetStage(name)
	if !ok {
		t.Fatal("GetStage returned false after RegisterStage")
	}
	if got.Name() != name {
		t.Errorf("stage.Name() = %q, want %q", got.Name(), name)
	}
}

func TestRegisterStage_DuplicatePanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for duplicate registration")
		}
	}()
	RegisterStage("dup_stage", "", func() Stage { return &mockStage{name: "dup"} })
	RegisterStage("dup_stage", "", func() Stage { return &mockStage{name: "dup"} })
}

func TestRegisterStage_EmptyNamePanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic for empty name")
		}
	}()
	RegisterStage("", "", func() Stage { return &mockStage{} })
}

func TestGetStage_NotFound(t *testing.T) {
	_, ok := GetStage("nonexistent_stage_12345")
	if ok {
		t.Fatal("GetStage should return false for unknown stage")
	}
}

func TestListStages(t *testing.T) {
	nm := "list_stages_test"
	RegisterStage(nm, "hello", func() Stage { return &mockStage{name: nm} })
	stages := ListStages()
	if _, ok := stages[nm]; !ok {
		t.Errorf("ListStages() missing key %q", nm)
	}
	if stages[nm] != "hello" {
		t.Errorf("ListStages()[%q] = %q, want %q", nm, stages[nm], "hello")
	}
}
