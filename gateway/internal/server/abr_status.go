package server

// These messages describe verified workflow states, never raw upstream errors.
func abrStatusMessage(status, code string) string {
	if status == "admission_rejected" {
		return "Payment admission rejected; settlement pending. Encoding has not started."
	}
	if status == "failed" && code == "not_admitted" {
		return "Payment admission rejected. Settlement is complete; no encoding work was admitted."
	}
	return ""
}
