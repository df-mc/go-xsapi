package xal

import (
	"context"
	"net/http"
)

// Config represents the basic configuration used to authenticate with Xbox Live
// services. All fields are specific or bound to the title and cannot be easily changed.
type Config struct {
	// Device identifies the device type used during authentication.
	// It is included in device token requests and determines how Xbox
	// Live services identify the platform.
	Device Device

	// UserAgent is the value sent as the "User-Agent" header on requests
	// to Xbox Live authentication services. It should be identically the
	// same as the one observed in the real application.
	UserAgent string

	// TitleID is the ID of the title to authenticate as. It is required
	// when requesting title tokens or initiating the Auth Code Flow via
	// SISU. The title ID may be different per platform in the same game.
	TitleID int64

	// Sandbox is the sandbox ID used to isolate test data from production.
	// It is usually "RETAIL" for most retail games available in Xbox Live.
	Sandbox string
}

// contextKey is an unexported type for context key used in HTTPClient.
type contextKey struct{}

// HTTPClient is the context key used for specifying an [http.Client] in a [context.Context]
// passed to the API call.
var HTTPClient contextKey

// ContextClient returns an [http.Client] from the [context.Context] if possible,
// otherwise it returns [http.DefaultClient].
func ContextClient(ctx context.Context) *http.Client {
	if value, ok := ctx.Value(HTTPClient).(*http.Client); ok {
		return value
	}
	return http.DefaultClient
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
