package config

// HTTP server config
type HTTP struct {
	Hostname string `yaml:"hostname"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
}
