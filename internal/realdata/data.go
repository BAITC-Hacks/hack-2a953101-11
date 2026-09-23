// Package realdata contains the reproducible snapshot of the supplied Excel files.
// Regenerate with python3 scripts/import_excel.py.
package realdata

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"github.com/electrokomplekt/replenishment/internal/planning"
)

//go:embed dataset.json.gz
var archive []byte

func Load() (planning.Dataset, error) {
	var d planning.Dataset
	r, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return d, err
	}
	defer r.Close()
	if err = json.NewDecoder(r).Decode(&d); err != nil {
		return d, err
	}
	return d, d.Validate()
}
