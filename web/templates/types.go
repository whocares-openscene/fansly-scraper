package templates

type Model struct {
	ID          string
	Username    string
	DisplayName string
	Walls       []Wall
}

type Wall struct {
	ID          string
	Name        string
	Description string
	Pos         int
}

type Flash struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type JobProgress struct {
	Type    string `json:"type"`
	Percent int    `json:"percent"`
	Message string `json:"message"`
	Current int    `json:"current"`
	Total   int    `json:"total"`
}