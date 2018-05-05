package server

import (
	"fmt"
	"net/http"
	"os"

	"github.com/cardigann/cardigann/config"
	"github.com/cardigann/cardigann/indexer"
	"github.com/cardigann/cardigann/logger"
)

// Server is an http server which wraps the Handler
type Server struct {
	Bind, Port, Passphrase string
	PathPrefix             string
	Hostname               string
	version                string
	config                 config.Config
}

func New(conf config.Config, version string) (*Server, error) {
	bind, err := config.GetGlobalConfig("bind", "0.0.0.0", conf)
	if err != nil {
		return nil, err
	}

	port, err := config.GetGlobalConfig("port", "5060", conf)
	if err != nil {
		return nil, err
	}

	prefix, err := config.GetGlobalConfig("pathprefix", "", conf)
	if err != nil {
		return nil, err
	}

	passphrase, err := config.GetGlobalConfig("passphrase", "", conf)
	if err != nil {
		return nil, err
	}

	if version == "" {
		version = "dev"
	}

	return &Server{
		Hostname:   "localhost",
		Bind:       bind,
		Port:       port,
		Passphrase: passphrase,
		PathPrefix: prefix,
		config:     conf,
		version:    version,
	}, nil
}

func (s *Server) Listen() error {
	logger.Logger.Infof("Cardigann %s", s.version)

	path, err := config.GetConfigPath()
	if err != nil {
		return err
	}

	logger.Logger.Infof("Reading config from %s", path)
	logger.Logger.Debugf("Cache dir is %s", config.GetCachePath("/"))

	for _, dir := range config.GetDefinitionDirs() {
		if _, err := os.Stat(dir); os.IsExist(err) {
			logger.Logger.Infof("Loading definitions from %s", dir)
		}
	}

	builtins, err := indexer.ListBuiltins()
	if err != nil {
		return err
	}

	logger.Logger.Debugf("Found %d built-in definitions", len(builtins))

	defs, err := indexer.DefaultDefinitionLoader.List()
	if err != nil {
		return err
	}

	active := 0
	for _, key := range defs {
		if config.IsSectionEnabled(key, s.config) {
			active++
		}
	}

	logger.Logger.Infof("Found %d indexers enabled in configuration", active)

	listenOn := fmt.Sprintf("%s:%s", s.Bind, s.Port)
	logger.Logger.Infof("Listening on %s", listenOn)

	h, err := NewHandler(Params{
		BaseURL:    fmt.Sprintf("http://%s:%s%s", s.Hostname, s.Port, s.PathPrefix),
		Passphrase: s.Passphrase,
		PathPrefix: s.PathPrefix,
		Config:     s.config,
		Version:    s.version,
	})
	if err != nil {
		return err
	}

	return http.ListenAndServe(listenOn, h)
}
