package frontend

import "testing"

func TestCellularInterfacesAreNeverAdvertisedAsLAN(t *testing.T) {
	for _, name := range []string{"rmnet_data0", "ccmni0", "pdp0", "wwan0", "v4-rmnet0", "r_rmnet_data0"} {
		if !isCellularInterface(name) {
			t.Errorf("%q must be classified as cellular", name)
		}
	}
	for _, name := range []string{"wlan0", "ap0", "eth0", "rndis0"} {
		if isCellularInterface(name) {
			t.Errorf("%q must remain an allowed direct-LAN interface", name)
		}
	}
}
