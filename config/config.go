package config

import (
	"encoding/json"
	"io/ioutil"
)

// Config contains the configuration of the url shortener.
type Config struct {
	Mongo struct {
		Host       string `json:"host"`
		Port       string `json:"port"`
		DB         string `json:"db"`
		Collection string `json:"collection"`
	} `json:"mongo"`
}

// FromFile returns a configuration parsed from the given file.
func FromFile(path string) (*Config, error) {
	b, err := ioutil.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var cfg Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}
