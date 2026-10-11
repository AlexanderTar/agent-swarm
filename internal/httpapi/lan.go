package httpapi

// LANStatus is LAN access as `swarm lan status` shows it.
type LANStatus struct {
	Enabled   bool   `json:"enabled"`
	State     string `json:"state"` // disabled | waiting | listening | error
	IP        string `json:"ip,omitempty"`
	Hostname  string `json:"hostname,omitempty"` // swarm.local, once Bonjour has registered it
	URL       string `json:"url,omitempty"`      // what to open on the phone
	LastError string `json:"last_error,omitempty"`
}
