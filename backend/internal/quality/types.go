package quality

type Rule struct {
	Name     string `json:"name"`
	Pattern  string `json:"pattern"`
	Score    int    `json:"score"`
	Required bool   `json:"required"`
	Negate   bool   `json:"negate"`
}

type Profile struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Qualities   []string `json:"qualities"`
	Cutoff      string   `json:"cutoff"`
	Upgrade     bool     `json:"upgrade"`
	MinMB       float64  `json:"minMB"`
	MaxMB       float64  `json:"maxMB"`
	Language    string   `json:"language"`
	MinScore    int      `json:"minScore"`
	CutoffScore int      `json:"cutoffScore"`
	Rules       []Rule   `json:"rules"`
}

type Details struct {
	Quality    string `json:"quality"`
	Resolution int    `json:"resolution"`
	Source     string `json:"source"`
	Codec      string `json:"codec"`
	Audio      string `json:"audio"`
	HDR        string `json:"hdr"`
	Language   string `json:"language"`
	Group      string `json:"group"`
	Edition    string `json:"edition"`
	Proper     bool   `json:"proper"`
}

type Current struct {
	Quality string `json:"quality"`
	Score   int    `json:"score"`
}

type Decision struct {
	Details Details  `json:"details"`
	Score   int      `json:"score"`
	Rank    int      `json:"rank"`
	Allowed bool     `json:"allowed"`
	Upgrade bool     `json:"upgrade"`
	Reasons []string `json:"reasons"`
}
