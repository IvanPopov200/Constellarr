package downloads

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/IvanPopov200/Constellarr/backend/internal/usenet"
)

type Config struct {
	IndexerURL string
	APIKey     string
	Directory  string
	Usenet     usenet.Config
}

func FromEnv() (Config, error) {
	port, err := envInt("USENET_PORT", 563, 1, 65535)
	if err != nil {
		return Config{}, err
	}
	connections, err := envInt("USENET_CONNECTIONS", 8, 1, 32)
	if err != nil {
		return Config{}, err
	}
	directory, err := filepath.Abs(env("DOWNLOAD_DIR", "data"))
	if err != nil {
		return Config{}, errors.New("DOWNLOAD_DIR is invalid")
	}
	var fallbacks []string
	for _, host := range strings.Split(os.Getenv("USENET_FALLBACK_HOSTS"), ",") {
		if host = strings.TrimSpace(host); host != "" {
			fallbacks = append(fallbacks, host)
		}
	}
	return Config{
		IndexerURL: env("NZBGEEK_URL", "https://api.nzbgeek.info/api"),
		APIKey:     os.Getenv("NZBGEEK_API_KEY"), Directory: directory,
		Usenet: usenet.Config{
			Host: env("USENET_HOST", "eunews.frugalusenet.com"), Port: port,
			Username: os.Getenv("USENET_USERNAME"), Password: os.Getenv("USENET_PASSWORD"),
			Connections: connections, FallbackHosts: fallbacks,
		},
	}, nil
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func envInt(name string, fallback, min, max int) (int, error) {
	value, err := strconv.Atoi(env(name, strconv.Itoa(fallback)))
	if err != nil || value < min || value > max {
		return 0, errors.New(name + " is outside its allowed range")
	}
	return value, nil
}
