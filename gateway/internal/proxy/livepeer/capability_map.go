package livepeer

// CapabilityMap maps product surfaces to capability identities and protocols.
// Discovery remains authoritative for supported offerings and their axes.
type CapabilityMap struct {
	ABR  CapabilitySpec
	Live CapabilitySpec
}
type CapabilitySpec struct {
	Capability      string
	DefaultOffering string
	Protocol        string
}

func NewDefault(abrCapability, liveCapability string) CapabilityMap {
	return CapabilityMap{
		ABR:  CapabilitySpec{Capability: abrCapability, DefaultOffering: "abr-default", Protocol: ProtocolJobV1},
		Live: CapabilitySpec{Capability: liveCapability, DefaultOffering: "gateway-ingest", Protocol: ProtocolSessionV1},
	}
}
