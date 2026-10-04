package metadata

type Title struct {
	IMDbID        string   `json:"imdbId"`
	Title         string   `json:"title"`
	Year          int      `json:"year"`
	Type          string   `json:"type"`
	TotalSeasons  int      `json:"totalSeasons,omitempty"`
	Released      string   `json:"released"`
	Rating        *float64 `json:"rating"`
	Votes         int      `json:"votes"`
	Runtime       int      `json:"runtime"`
	Directors     []string `json:"directors"`
	Cast          []string `json:"cast"`
	Genres        []string `json:"genres"`
	Languages     []string `json:"languages"`
	Countries     []string `json:"countries"`
	Certification string   `json:"certification"`
	Poster        string   `json:"poster"`
	Plot          string   `json:"plot"`
}

type Episode struct {
	IMDbID  string   `json:"imdbId"`
	Title   string   `json:"title"`
	Season  int      `json:"season"`
	Number  int      `json:"number"`
	AirDate string   `json:"airDate"`
	Rating  *float64 `json:"rating"`
}
