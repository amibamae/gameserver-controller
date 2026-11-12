package controllersetup

// Setup loads controller configurations that may select built-in adapter
// implementations. Importing controllerruntime registers them, one file per
// adapter (internal/controllerruntime/adapter_*.go).
import _ "github.com/amibamae/gameserver-controller/internal/controllerruntime"
