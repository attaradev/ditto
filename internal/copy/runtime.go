package copy

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/attaradev/ditto/engine"
)

type AccessMode string

const (
	AccessModeLocal  AccessMode = "local"
	AccessModeRemote AccessMode = "remote"
)

// RemoteRuntimeConfig controls how copies are exposed when ditto runs as a shared host.
type RemoteRuntimeConfig struct {
	Mode          AccessMode
	AdvertiseHost string
	BindHost      string
	CopySecret    string
	TLSEnabled    bool
	CertFile      string
	KeyFile       string
}

// CopyRuntime holds the engine bootstrap and the internal/external connection parameters
// for a single copy.
type CopyRuntime struct {
	Bootstrap engine.CopyBootstrap
	Internal  engine.ConnectionConfig
	External  engine.ConnectionConfig
	BindHost  string
}

func localRuntime(port int) CopyRuntime {
	bootstrap := engine.DefaultLocalBootstrap()
	conn := engine.ConnectionConfig{
		Host:     "localhost",
		Port:     port,
		Database: bootstrap.Database,
		User:     bootstrap.User,
		Password: bootstrap.Password,
	}
	return CopyRuntime{
		Bootstrap: bootstrap,
		Internal:  conn,
		External:  conn,
		BindHost:  "127.0.0.1",
	}
}

func remoteRuntime(port int, cfg RemoteRuntimeConfig, copyID string) (CopyRuntime, error) {
	if cfg.AdvertiseHost == "" {
		return CopyRuntime{}, fmt.Errorf("remote runtime: advertise host is required")
	}
	if cfg.BindHost == "" {
		return CopyRuntime{}, fmt.Errorf("remote runtime: bind host is required")
	}
	if cfg.CopySecret == "" {
		return CopyRuntime{}, fmt.Errorf("remote runtime: copy secret is required")
	}

	bootstrap := engine.CopyBootstrap{
		Database:     "ditto",
		User:         derivedUsername(copyID),
		Password:     deriveSecret(cfg.CopySecret, copyID, "user"),
		RootPassword: deriveSecret(cfg.CopySecret, copyID, "root"),
		TLSEnabled:   cfg.TLSEnabled,
	}
	internal := engine.ConnectionConfig{
		Host:       cfg.AdvertiseHost,
		Port:       port,
		Database:   bootstrap.Database,
		User:       bootstrap.User,
		Password:   bootstrap.Password,
		TLSEnabled: cfg.TLSEnabled,
	}
	return CopyRuntime{
		Bootstrap: bootstrap,
		Internal:  internal,
		External:  internal,
		BindHost:  cfg.BindHost,
	}, nil
}

func derivedUsername(copyID string) string {
	suffix := strings.ToLower(copyID)
	if len(suffix) > 12 {
		suffix = suffix[len(suffix)-12:]
	}
	return "ditto_" + suffix
}

func deriveSecret(secret, copyID, purpose string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(copyID))
	mac.Write([]byte{0})
	mac.Write([]byte(purpose))
	return hex.EncodeToString(mac.Sum(nil))[:32]
}
