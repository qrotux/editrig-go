package editrig

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func fullEntity(name string) Entity {
	return Entity{
		Name: name,
		Schema: func(ctx context.Context, cat Catalog, data map[string]any) (json.RawMessage, json.RawMessage, error) {
			return json.RawMessage(`{}`), json.RawMessage(`{}`), nil
		},
		Load: func(ctx context.Context, id string) (map[string]any, error) {
			return nil, nil
		},
		Save: func(ctx context.Context, id *string, in map[string]any) (string, []FieldError, error) {
			return "1", nil, nil
		},
		Delete: func(ctx context.Context, id string) error {
			return nil
		},
		Validate: func(ctx context.Context, cat Catalog, id *string, in map[string]any) []FieldError {
			return nil
		},
	}
}

func TestNewRegistry(t *testing.T) {
	tests := []struct {
		name     string
		entities []Entity
		wantErr  bool
		wantSub  string // substring the error must contain
	}{
		{
			name:     "fully populated entity — no error",
			entities: []Entity{fullEntity("widgets")},
			wantErr:  false,
		},
		{
			name:     "empty name",
			entities: []Entity{fullEntity("")},
			wantErr:  true,
			wantSub:  "empty name",
		},
		{
			name:     "duplicate name",
			entities: []Entity{fullEntity("widgets"), fullEntity("widgets")},
			wantErr:  true,
			wantSub:  "widgets",
		},
		{
			name: "nil Schema",
			entities: []Entity{func() Entity {
				e := fullEntity("widgets")
				e.Schema = nil
				return e
			}()},
			wantErr: true,
			wantSub: "widgets",
		},
		{
			name: "nil Load",
			entities: []Entity{func() Entity {
				e := fullEntity("widgets")
				e.Load = nil
				return e
			}()},
			wantErr: true,
			wantSub: "widgets",
		},
		{
			name: "nil Save",
			entities: []Entity{func() Entity {
				e := fullEntity("widgets")
				e.Save = nil
				return e
			}()},
			wantErr: false,
			wantSub: "widgets",
		},
		{
			name: "nil Delete",
			entities: []Entity{func() Entity {
				e := fullEntity("widgets")
				e.Delete = nil
				return e
			}()},
			wantErr: false,
			wantSub: "widgets",
		},
		{
			name: "nil Validate",
			entities: []Entity{func() Entity {
				e := fullEntity("widgets")
				e.Validate = nil
				return e
			}()},
			wantErr: false,
			wantSub: "widgets",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg, err := NewRegistry(tt.entities...)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if tt.wantSub != "" && !strings.Contains(err.Error(), tt.wantSub) {
					t.Fatalf("error %q does not contain %q", err.Error(), tt.wantSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if reg == nil {
				t.Fatalf("expected non-nil registry")
			}
			e, ok := reg.Get(tt.entities[0].Name)
			if !ok || e == nil {
				t.Fatalf("expected registered entity %q to be retrievable", tt.entities[0].Name)
			}
		})
	}
}

// TestRegistryNames pins membership AND order: Names walks a map, which Go
// randomizes, so unsorted a registry-walking test would report in a
// flickering order. Registration here is deliberately in reverse alphabetical
// order: were it preserved, the slice would come back the same way.
func TestRegistryNames(t *testing.T) {
	reg, err := NewRegistry(fullEntity("widgets"), fullEntity("gadgets"), fullEntity("countries"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := reg.Names()
	want := []string{"countries", "gadgets", "widgets"}
	if len(got) != len(want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Names() = %v, want %v", got, want)
		}
	}

	empty, err := NewRegistry()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Callers write `for _, n := range reg.Names()`, so the difference between
	// an empty slice and nil must stay insignificant.
	if names := empty.Names(); len(names) != 0 {
		t.Fatalf("Names() on empty registry = %v, want none", names)
	}
}

func TestRegistryGetUnknown(t *testing.T) {
	reg, err := NewRegistry(fullEntity("widgets"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := reg.Get("does-not-exist"); ok {
		t.Fatalf("expected unknown entity lookup to fail")
	}
}
