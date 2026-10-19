module p2plink

go 1.25.0

require (
	fyne.io/systray v1.12.2
	github.com/webview/webview_go v0.0.0-20240831120633-6173450d4dd6
	github.com/xtaci/kcp-go/v5 v5.6.72
	github.com/xtaci/smux v1.5.57
	golang.org/x/crypto v0.45.0
	golang.org/x/sys v0.38.0
	golang.org/x/time v0.14.0
)

require (
	github.com/godbus/dbus/v5 v5.1.0 // indirect
	github.com/klauspost/cpuid/v2 v2.2.6 // indirect
	github.com/klauspost/reedsolomon v1.12.0 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	github.com/stretchr/testify v1.11.1 // indirect
	github.com/tjfoc/gmsm v1.4.1 // indirect
	golang.org/x/net v0.47.0 // indirect
)

replace github.com/webview/webview_go => ./third_party/webview_go
