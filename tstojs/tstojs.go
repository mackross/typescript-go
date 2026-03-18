package tstojs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

var ErrNotImplemented = errors.New("tstojs: schema generation not implemented")

type Fixture struct {
	Name string
	Dir  string
}

func DiscoverFixtures(root string) ([]Fixture, error) {
	var fixtures []Fixture

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}

		mainPath := filepath.Join(path, "main.ts")
		schemaPath := filepath.Join(path, "schema.json")

		if _, err := os.Stat(mainPath); err != nil {
			return nil
		}
		if _, err := os.Stat(schemaPath); err != nil {
			return nil
		}

		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}

		fixtures = append(fixtures, Fixture{
			Name: rel,
			Dir:  path,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Slice(fixtures, func(i, j int) bool {
		return fixtures[i].Name < fixtures[j].Name
	})

	return fixtures, nil
}

func GenerateFixture(_ context.Context, fixture Fixture) ([]byte, error) {
	return nil, fmt.Errorf("%w: %s", ErrNotImplemented, fixture.Name)
}
