//go:build !windows

package shellenv

func RunWindowsCooperativeCommandHelper(args []string) (bool, int, error) {
	return false, 0, nil
}
