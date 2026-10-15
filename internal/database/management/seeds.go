package management

import (
	"fmt"
	"io/fs"
	"path"
	"strings"
)

type SeedGroup string

const (
	SeedGroupCatalog SeedGroup = "catalog"
	SeedGroupFixture SeedGroup = "fixture"
)

type SeedDefinition struct {
	Name  string
	File  string
	Group SeedGroup
}

// Seeds run in declaration order and are identified by their unique Name.
// Keep applied names unchanged and append new seeds to this list.
var seeds = []SeedDefinition{
	{
		Name:  "foo_initial_data",
		File:  "seeds/fixture_foo.sql",
		Group: SeedGroupFixture,
	},
}

// Validate checks the environment and embedded seed index without opening a database.
func Validate(environment string) error {
	_, err := validateIndex(environment, files, seeds)
	return err
}

func validateIndex(environment string, source fs.FS, index []SeedDefinition) ([]SeedDefinition, error) {
	switch environment {
	case "development", "test", "production":
	default:
		return nil, fmt.Errorf("APP_ENV must be development, test, or production")
	}
	names := make(map[string]bool)
	registered := make(map[string]bool)
	for _, seed := range index {
		if strings.TrimSpace(seed.Name) == "" || names[seed.Name] {
			return nil, fmt.Errorf("empty or duplicate seed name: %q", seed.Name)
		}
		if seed.Group != SeedGroupCatalog && seed.Group != SeedGroupFixture {
			return nil, fmt.Errorf("unknown group %q for seed %s", seed.Group, seed.Name)
		}
		if !fs.ValidPath(seed.File) || path.Dir(seed.File) != "seeds" || path.Ext(seed.File) != ".sql" || registered[seed.File] {
			return nil, fmt.Errorf("invalid or duplicate seed file: %q", seed.File)
		}
		if info, err := fs.Stat(source, seed.File); err != nil || info.IsDir() {
			return nil, fmt.Errorf("seed file not found: %s", seed.File)
		}
		names[seed.Name], registered[seed.File] = true, true
	}
	if err := fs.WalkDir(source, "seeds", func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && path.Ext(file) == ".sql" && !registered[file] {
			return fmt.Errorf("seed SQL file is not registered: %s", file)
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("validate seed files: %w", err)
	}
	return append([]SeedDefinition(nil), index...), nil
}
