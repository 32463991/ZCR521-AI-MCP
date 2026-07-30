//go:build windows

package broker

func chownSocket(string, int) error {
	return nil
}
