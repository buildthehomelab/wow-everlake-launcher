package install

import (
	"context"
	"io/fs"
	"path/filepath"
)

type InventoryReport struct {
	Files int
	Bytes int64
}

// Inventory measures regular files without changing client data.
func Inventory(ctx context.Context, root string) (InventoryReport, error) {
	var report InventoryReport
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if d.Type().IsRegular() {
			info, err := d.Info()
			if err != nil {
				return err
			}
			report.Files++
			report.Bytes += info.Size()
		}
		return nil
	})
	return report, err
}
