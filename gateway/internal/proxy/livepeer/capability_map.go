package livepeer

// InteractionMode constants from livepeer-network-protocol.
const (
	ModeHTTPReqResp        = "http-reqresp@v0"
	ModeRTMPIngressHLSOut  = "rtmp-ingress-hls-egress@v0"
)

// CapabilityMap describes how a product surface (ABR job, live session)
// translates to a registry capability + a broker interaction mode.
//
// Each entry is independent of the others; adding a new product surface
// is one row here plus the matching handler.
type CapabilityMap struct {
	ABR  CapabilitySpec
	Live CapabilitySpec
}

type CapabilitySpec struct {
	Capability      string // e.g. "livepeer:transcode/abr-ladder"
	DefaultOffering string // e.g. "default"
	InteractionMode string // e.g. "http-reqresp@v0"
}

// NewDefault constructs the capability map from configured capability
// identifiers (env-overridable).
func NewDefault(abrCapability, liveCapability string) CapabilityMap {
	return CapabilityMap{
		ABR: CapabilitySpec{
			Capability:      abrCapability,
			DefaultOffering: "default",
			InteractionMode: ModeHTTPReqResp,
		},
		Live: CapabilitySpec{
			Capability:      liveCapability,
			DefaultOffering: "default",
			InteractionMode: ModeRTMPIngressHLSOut,
		},
	}
}
