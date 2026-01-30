package xal

import "github.com/yomoggies/xsapi-go/xal/internal"

var HTTPClient = internal.HTTPClient

type Config struct {
	Device    Device
	UserAgent string
	TitleID   int64
}

// Device describes the platform and operating system of the device being
// authenticated in [xasd.Authenticate] method.
type Device struct {
	// Type identifies the category of device being authenticated.
	//
	// It must be set to one of the DeviceType* constants defined in this
	// package
	Type string

	// Version specifies the operating system version of the device.
	//
	// The expected format and meaning of this field depend on the value
	// of Type. In general, this should match the version string reported
	// by the underlying platform.
	Version string
}

const (
	// DeviceTypeAndroid indicates that the device is running Android.
	//
	// For this device type, Version should be the Android OS version,
	// typically expressed as a major version number (for example, "13"
	// or "14").
	DeviceTypeAndroid = "Android"

	// DeviceTypeIOS indicates that the device is running iOS or iPadOS.
	//
	// For this device type, Version should be the iOS version number
	// (for example, "16.6" or "26.2").
	DeviceTypeIOS = "iOS"

	// DeviceTypeNintendo indicates that the device is a Nintendo Switch
	// console.
	//
	// For this device type, Version should be the version of the Nintendo
	// Switch system software (also known as Horizon OS), typically in
	// dotted numeric form (for example, "17.0.1").
	//
	// Official version changelogs:
	//  - Nintendo Switch: https://en-americas-support.nintendo.com/app/answers/detail/a_id/43314
	//  - Nintendo Switch 2: https://en-americas-support.nintendo.com/app/answers/detail/a_id/68526
	DeviceTypeNintendo = "Nintendo"

	// DeviceTypePlayStation indicates that the device is a PlayStation
	// console.
	//
	// For this device type, Version should be the system software version
	// of the console. This corresponds to Orbis OS on PlayStation 4 and
	// PlayStation 5.
	//
	// Official version changelogs:
	//   - PlayStation 4: https://www.playstation.com/en-us/support/hardware/ps4/system-software-info/
	//   - PlayStation 5: https://www.playstation.com/en-us/support/hardware/ps5/system-software-info/
	DeviceTypePlayStation = "PlayStation"

	// DeviceTypeWin32 indicates that the device is running Microsoft Windows.
	//
	// This includes both Windows 10 and Windows 11 systems. For this device
	// type, Version should be the Windows NT kernel version, which begins
	// at "10.0" for both Windows 10 and Windows 11 (for example,
	// "10.0.19045" or "10.0.22621").
	DeviceTypeWin32 = "Win32"
)
