package livepeer

import "net/http"

// Standard Livepeer-* headers. Names mirror @tztcloud/livepeer-gateway-middleware.
const (
	HeaderCapability  = "Livepeer-Capability"
	HeaderOffering    = "Livepeer-Offering"
	HeaderPayment     = "Livepeer-Payment"
	HeaderRequestID   = "Livepeer-Request-Id"
	HeaderMode        = "Livepeer-Mode"
	HeaderSpecVersion = "Livepeer-Spec-Version"
	HeaderWorkUnits   = "Livepeer-Work-Units"
)

// SpecVersion is the Livepeer-Spec-Version header value the broker expects.
const SpecVersion = "0.1"

// SetWireHeaders writes the standard Livepeer-* envelope onto an
// outbound broker request.
func SetWireHeaders(h http.Header, capability, offering, mode, requestID string, payment []byte) {
	if capability != "" {
		h.Set(HeaderCapability, capability)
	}
	if offering != "" {
		h.Set(HeaderOffering, offering)
	}
	if mode != "" {
		h.Set(HeaderMode, mode)
	}
	h.Set(HeaderSpecVersion, SpecVersion)
	if requestID != "" {
		h.Set(HeaderRequestID, requestID)
	}
	if len(payment) > 0 {
		// The payer-daemon emits wire-format Payment bytes; the broker
		// decodes the header as base64. Keep encoding consistent with the
		// rest of the Livepeer ecosystem (RFC 4648 §4 standard encoding).
		h.Set(HeaderPayment, encodePayment(payment))
	}
}

func encodePayment(b []byte) string {
	// The Livepeer convention is to base64-encode the wire-format Payment bytes
	// into the header value. Keep this in one place so the encoding stays
	// consistent with what the broker expects.
	return base64Encode(b)
}
