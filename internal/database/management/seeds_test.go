package management

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"
)

func TestEmbeddedIndexAndEnvironments(t *testing.T) {
	for _, environment := range []string{"development", "test", "production"} {
		if err := Validate(environment); err != nil {
			t.Fatalf("%s: %v", environment, err)
		}
	}
	for _, environment := range []string{"", "staging", "Production"} {
		// Invalid configuration must fail before accessing even a nil DB or logger.
		if err := Initialize(context.Background(), nil, environment, nil); err == nil {
			t.Fatalf("accepted unknown environment %q", environment)
		}
	}
}

func TestInvalidSeedIndex(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(fstest.MapFS, *[]SeedDefinition)
		want   string
	}{
		{"empty name", func(_ fstest.MapFS, index *[]SeedDefinition) { (*index)[0].Name = " " }, "name"},
		{"duplicate name", func(_ fstest.MapFS, index *[]SeedDefinition) {
			*index = append(*index, SeedDefinition{Name: (*index)[0].Name})
		}, "name"},
		{"unknown group", func(_ fstest.MapFS, index *[]SeedDefinition) { (*index)[0].Group = "unknown" }, "group"},
		{"missing file", func(source fstest.MapFS, _ *[]SeedDefinition) { delete(source, seeds[0].File) }, "not found"},
		{"directory instead of file", func(source fstest.MapFS, _ *[]SeedDefinition) {
			delete(source, seeds[0].File)
			source[seeds[0].File+"/child"] = &fstest.MapFile{}
		}, "not found"},
		{"unregistered SQL", func(source fstest.MapFS, _ *[]SeedDefinition) {
			source["seeds/catalog_extra.sql"] = &fstest.MapFile{}
		}, "not registered"},
		{"nested unregistered SQL", func(source fstest.MapFS, _ *[]SeedDefinition) {
			source["seeds/nested/catalog_extra.sql"] = &fstest.MapFile{}
		}, "not registered"},
		{"duplicate file", func(_ fstest.MapFS, index *[]SeedDefinition) {
			duplicate := (*index)[0]
			duplicate.Name = "another_seed"
			*index = append(*index, duplicate)
		}, "file"},
		{"path traversal", func(_ fstest.MapFS, index *[]SeedDefinition) { (*index)[0].File = "seeds/../secret.sql" }, "file"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := fstest.MapFS{seeds[0].File: &fstest.MapFile{Data: []byte("SELECT 1;")}}
			index := append([]SeedDefinition(nil), seeds...)
			tc.change(source, &index)
			if _, err := validateIndex("test", source, index); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q validation error, got %v", tc.want, err)
			}
		})
	}
}

func TestSeedIndexPreservesDeclarationOrder(t *testing.T) {
	source := fstest.MapFS{
		"seeds/fixture_foo.sql":    &fstest.MapFile{},
		"seeds/catalog_status.sql": &fstest.MapFile{},
	}
	index := []SeedDefinition{
		{Name: "statuses", File: "seeds/catalog_status.sql", Group: SeedGroupCatalog},
		seeds[0],
	}
	ordered, err := validateIndex("production", source, index)
	if err != nil {
		t.Fatal(err)
	}
	if ordered[0].Name != "statuses" || ordered[1].Name != "foo_initial_data" {
		t.Fatal("seed declaration order was not preserved")
	}
	ordered[0].Name = "changed_copy"
	if index[0].Name != "statuses" {
		t.Fatal("validation returned the original registry instead of a copy")
	}
}
