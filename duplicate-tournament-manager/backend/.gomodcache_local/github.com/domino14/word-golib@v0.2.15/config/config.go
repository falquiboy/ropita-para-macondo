package config

import "os"

type Config struct {
	DataPath      string
	KWGPathPrefix string
}

var DefaultConfig = &Config{
	DataPath: os.Getenv("DATA_PATH"),
}
