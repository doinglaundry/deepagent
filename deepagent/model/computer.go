package model

// ComputerAction is the JSON argument of a browser/desktop operation, not a Graph request.
type ComputerAction struct {
	URL           string   `json:"url,omitempty"`
	App           string   `json:"app,omitempty"`
	ObservationID string   `json:"observation_id,omitempty"`
	ElementID     int      `json:"element_id,omitempty"`
	Text          string   `json:"text,omitempty"`
	Key           string   `json:"key,omitempty"`
	X             *float64 `json:"x,omitempty"`
	Y             *float64 `json:"y,omitempty"`
	DeltaX        int      `json:"delta_x,omitempty"`
	DeltaY        int      `json:"delta_y,omitempty"`
}

type ComputerElement struct {
	ID     int     `json:"element_id"`
	Role   string  `json:"role"`
	Name   string  `json:"name"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// ComputerObservation is one actual screen and its accessible elements.
type ComputerObservation struct {
	ID       string            `json:"observation_id"`
	URL      string            `json:"url,omitempty"`
	App      string            `json:"app,omitempty"`
	Text     string            `json:"text"`
	Width    int               `json:"width"`
	Height   int               `json:"height"`
	ScrollY  float64           `json:"scroll_y,omitempty"`
	Elements []ComputerElement `json:"elements,omitempty"`
	Image    string            `json:"png,omitempty"`
}
