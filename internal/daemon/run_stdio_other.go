//go:build !windows

package daemon

func PrepareRunStdio() error {
	return nil
}
