package xal

type Config struct {
	Device    Device
	UserAgent string
	TitleID   int64
}

// Device represents the configuration of the device being authenticated in [Authenticate].
type Device struct {
	// Type indicates the type of the device.
	// It is one of the constants below.
	Type string
	// Version is the version of the device.
	// For example, if the device is running on Android 13, it will be '13'.
	Version string
}

const (
	// DeviceTypeAndroid indicates the Device is running Android.
	// The Version field for Device is the Android version. e.g. '13'.
	DeviceTypeAndroid = "Android"
	// DeviceTypeIOS indicates the Device is running iOS.
	// The Version field of Device is the version of iOS.
	DeviceTypeIOS = "iOS"
	// DeviceTypeNintendo indicates the Device is a Nintendo Switch console.
	// The Version field of Device is the version of Nintendo Switch OS, aka. Horizon.
	//
	// Version Changelogs can be viewed here:
	// Nintendo Switch: https://en-americas-support.nintendo.com/app/answers/detail/a_id/43314
	// Nintendo Switch 2: https://en-americas-support.nintendo.com/app/answers/detail/a_id/68526
	DeviceTypeNintendo = "Nintendo"
	// DeviceTypePlayStation indicates the Device is a PlayStation console.
	// The Version field of Device is the version of PlayStation 4 OS, aka. Orbis OS.
	// Version Changelogs can be viewed here:
	// PlayStation 4: https://www.playstation.com/en-us/support/hardware/ps4/system-software-info/
	// PlayStation 5: https://www.playstation.com/en-us/support/hardware/ps5/system-software-info/
	DeviceTypePlayStation = "PlayStation"
	// DeviceTypeWin32 indicates the Device is running either Windows 10/11.
	// The [Device.Version] field will be the version of Windows NT kernel running on the device,
	// which is starting from 10.00 for both Windows 10 and 11 devices.
	DeviceTypeWin32 = "Win32"
)
