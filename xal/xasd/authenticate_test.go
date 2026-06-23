package xasd

import (
	"strings"
	"testing"

	"github.com/df-mc/go-xsapi/v2/xal"
)

func TestDeviceID(t *testing.T) {
	t.Parallel()

	const id = "6f8b0a63-0947-46b6-8076-dc094aa65043"
	tests := []struct {
		name         string
		deviceType   string
		id           string
		serialNumber string
	}{
		{name: "android", deviceType: xal.DeviceTypeAndroid, id: "{" + id + "}"},
		{name: "nintendo", deviceType: xal.DeviceTypeNintendo, id: "{" + id + "}"},
		{name: "ios", deviceType: xal.DeviceTypeIOS, id: strings.ToUpper(id)},
		{name: "playstation", deviceType: xal.DeviceTypePlayStation, id: id},
		{name: "win32", deviceType: xal.DeviceTypeWin32, id: "{" + strings.ToUpper(id) + "}", serialNumber: "{" + strings.ToUpper(id) + "}"},
		{name: "xbox", deviceType: xal.DeviceTypeXbox, id: "{" + strings.ToUpper(id) + "}", serialNumber: "{" + strings.ToUpper(id) + "}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotID, gotSerialNumber, err := deviceID(tt.deviceType, id)
			if err != nil {
				t.Fatalf("deviceID: %v", err)
			}
			if gotID != tt.id {
				t.Fatalf("ID = %q, want %q", gotID, tt.id)
			}
			if gotSerialNumber != tt.serialNumber {
				t.Fatalf("SerialNumber = %q, want %q", gotSerialNumber, tt.serialNumber)
			}
		})
	}
}

func TestDeviceIDUnknownDeviceType(t *testing.T) {
	t.Parallel()

	_, _, err := deviceID("Unknown", "6f8b0a63-0947-46b6-8076-dc094aa65043")
	if err == nil || !strings.Contains(err.Error(), "unknown device type: Unknown") {
		t.Fatalf("deviceID error = %v, want unknown device type", err)
	}
}
