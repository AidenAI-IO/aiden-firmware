package agent

type observedWorldState struct {
	AppName     string   `json:"app_name,omitempty"`
	PageName    string   `json:"page_name,omitempty"`
	Platform    string   `json:"platform,omitempty"`
	VisibleText []string `json:"visible_text,omitempty"`
	Dialogs     []string `json:"dialogs,omitempty"`
	Confidence  float64  `json:"confidence,omitempty"`
}
