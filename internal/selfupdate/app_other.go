//go:build !darwin

package selfupdate

import "fmt"

func replaceApp(app, staged string, check func(program string) error) error {
	return fmt.Errorf("%s: only macOS updates a .app", app)
}
