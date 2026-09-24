package livepeer

// LiveControl contains the broker control URLs advertised at session open.
// Requests are constructed from the pinned broker route and session identity.
type LiveControl struct {
	TopupURL  string `json:"topup_url"`
	StatusURL string `json:"status_url"`
	EndURL    string `json:"end_url"`
	EventsWS  string `json:"events_ws"`
}
