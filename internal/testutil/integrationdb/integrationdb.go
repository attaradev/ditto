package integrationdb

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/attaradev/ditto/engine"
	_ "github.com/attaradev/ditto/engine/mysql"
	_ "github.com/attaradev/ditto/engine/postgres"
	"github.com/attaradev/ditto/internal/dockerutil"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/oklog/ulid/v2"
)

const (
	EnginePostgres = "postgres"
	EngineMySQL    = "mysql"
)

// Suite owns one Docker client and one isolated network for a single
// integration test case.
type Suite struct {
	t                 *testing.T
	ctx               context.Context
	Docker            *client.Client
	Engine            engine.Engine
	EngineName        string
	NetworkName       string
	hostAccessAddress string
}

// Database describes a running source or copy container managed by Suite.
type Database struct {
	Suite       *Suite
	Name        string
	ContainerID string
	Port        int
	Bootstrap   engine.CopyBootstrap
}

// NewSuite creates an isolated Docker network for one engine-specific test.
func NewSuite(t *testing.T, engineName string) *Suite {
	t.Helper()

	ctx := t.Context()
	docker, _, err := dockerutil.NewClient(ctx, "")
	if err != nil {
		t.Skipf("Docker not available: %v", err)
	}

	eng, err := engine.Get(engineName)
	if err != nil {
		t.Fatalf("engine.Get(%s): %v", engineName, err)
	}

	networkName := fmt.Sprintf("ditto-it-%s-%s", engineName, strings.ToLower(ulid.Make().String()))
	resp, err := docker.NetworkCreate(ctx, networkName, network.CreateOptions{})
	if err != nil {
		t.Fatalf("NetworkCreate %s: %v", networkName, err)
	}
	t.Cleanup(func() {
		_ = docker.NetworkRemove(context.Background(), resp.ID)
		_ = docker.Close()
	})

	return &Suite{
		t:           t,
		ctx:         ctx,
		Docker:      docker,
		Engine:      eng,
		EngineName:  engineName,
		NetworkName: networkName,
	}
}

// StartSource starts a source database container with a host-visible port and a
// network alias so tests can use either path.
func (s *Suite) StartSource() *Database {
	s.t.Helper()
	return s.startDatabase("src", "0.0.0.0", "", sourceBootstrap())
}

// StartCopy starts a copy or staging container with /dump mounted from dumpDir.
func (s *Suite) StartCopy(dumpDir string) *Database {
	s.t.Helper()
	return s.startDatabase("copy", "127.0.0.1", dumpDir, copyBootstrap())
}

// HostAccessAddress returns a non-loopback IP address that helper containers
// can use to reach services published on the host.
func (s *Suite) HostAccessAddress() string {
	s.t.Helper()

	if s.hostAccessAddress != "" {
		return s.hostAccessAddress
	}

	if override := strings.TrimSpace(os.Getenv("DITTO_TEST_HOST_IP")); override != "" {
		s.hostAccessAddress = override
		return override
	}

	ip, err := localIPv4()
	if err != nil {
		s.t.Fatalf("determine host access address: %v", err)
	}
	s.hostAccessAddress = ip
	return ip
}

func (s *Suite) startDatabase(prefix, bindHost, dumpDir string, bootstrap engine.CopyBootstrap) *Database {
	s.t.Helper()

	if err := dockerutil.EnsureImage(s.ctx, s.Docker, s.Engine.ContainerImage()); err != nil {
		s.t.Fatalf("pull image %s: %v", s.Engine.ContainerImage(), err)
	}

	name := fmt.Sprintf("ditto-it-%s-%s-%s", s.EngineName, prefix, strings.ToLower(ulid.Make().String()))
	hostPort := MustFreePort(s.t)
	exposedPort := nat.Port(fmt.Sprintf("%d/tcp", s.Engine.ContainerPort()))
	spec := s.Engine.ContainerSpec(bootstrap)

	var mounts []mount.Mount
	if dumpDir != "" {
		mounts = append(mounts, mount.Mount{
			Type:   mount.TypeBind,
			Source: dumpDir,
			Target: "/dump",
		})
	}

	resp, err := s.Docker.ContainerCreate(
		s.ctx,
		&container.Config{
			Image:        s.Engine.ContainerImage(),
			Env:          spec.Env,
			Cmd:          spec.Cmd,
			ExposedPorts: nat.PortSet{exposedPort: struct{}{}},
		},
		&container.HostConfig{
			PortBindings: nat.PortMap{
				exposedPort: []nat.PortBinding{{
					HostIP:   bindHost,
					HostPort: fmt.Sprintf("%d", hostPort),
				}},
			},
			Mounts: mounts,
		},
		&network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				s.NetworkName: {Aliases: []string{name}},
			},
		},
		nil,
		name,
	)
	if err != nil {
		s.t.Fatalf("ContainerCreate %s: %v", name, err)
	}
	if err := s.Docker.ContainerStart(s.ctx, resp.ID, container.StartOptions{}); err != nil {
		_ = s.Docker.ContainerRemove(context.Background(), resp.ID, container.RemoveOptions{Force: true})
		s.t.Fatalf("ContainerStart %s: %v", name, err)
	}
	s.t.Cleanup(func() {
		_ = s.Docker.ContainerStop(context.Background(), resp.ID, container.StopOptions{Timeout: intPtr(5)})
		_ = s.Docker.ContainerRemove(context.Background(), resp.ID, container.RemoveOptions{Force: true})
	})

	db := &Database{
		Suite:       s,
		Name:        name,
		ContainerID: resp.ID,
		Port:        hostPort,
		Bootstrap:   bootstrap,
	}
	if err := s.Engine.WaitReady(db.LocalConnection(), 3*time.Minute); err != nil {
		s.t.Fatalf("WaitReady %s: %v", name, err)
	}
	return db
}

// LocalConnection returns the host-local connection config for the running DB.
func (db *Database) LocalConnection() engine.ConnectionConfig {
	return engine.ConnectionConfig{
		Host:     "127.0.0.1",
		Port:     db.Port,
		Database: db.Bootstrap.Database,
		User:     db.Bootstrap.User,
		Password: db.Bootstrap.Password,
	}
}

// HostConnection returns a connection config pointing at host.
func (db *Database) HostConnection(host string) engine.ConnectionConfig {
	return engine.ConnectionConfig{
		Host:     host,
		Port:     db.Port,
		Database: db.Bootstrap.Database,
		User:     db.Bootstrap.User,
		Password: db.Bootstrap.Password,
	}
}

// LocalDSN returns the DSN the host can use to query the running DB.
func (db *Database) LocalDSN() string {
	return db.Suite.Engine.ConnectionString(db.LocalConnection())
}

// HostDSN returns the DSN helper containers can use to query the running DB.
func (db *Database) HostDSN(host string) string {
	return db.Suite.Engine.ConnectionString(db.HostConnection(host))
}

// NetworkSourceConfig returns a dump source config that resolves through the
// suite's private Docker network.
func (db *Database) NetworkSourceConfig() engine.SourceConfig {
	return engine.SourceConfig{
		Host:        db.Name,
		Port:        db.Suite.Engine.ContainerPort(),
		Database:    db.Bootstrap.Database,
		User:        db.Bootstrap.User,
		Password:    db.Bootstrap.Password,
		NetworkName: db.Suite.NetworkName,
	}
}

// HostSourceConfig returns a dump source config that reaches the published host
// port through host.
func (db *Database) HostSourceConfig(host string) engine.SourceConfig {
	return engine.SourceConfig{
		Host:     host,
		Port:     db.Port,
		Database: db.Bootstrap.Database,
		User:     db.Bootstrap.User,
		Password: db.Bootstrap.Password,
	}
}

// OpenDB opens a database/sql handle for dsn using the correct driver for
// engineName.
func OpenDB(t *testing.T, engineName, dsn string) *sql.DB {
	t.Helper()

	db, err := sql.Open(driverNameFor(engineName), dsn)
	if err != nil {
		t.Fatalf("sql.Open(%s): %v", engineName, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// MustFreePort returns an available TCP port on the host.
func MustFreePort(t *testing.T) int {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

func sourceBootstrap() engine.CopyBootstrap {
	return engine.CopyBootstrap{
		Database:     "srcdb",
		User:         "src",
		Password:     "src",
		RootPassword: "src-root",
	}
}

func copyBootstrap() engine.CopyBootstrap {
	return engine.CopyBootstrap{
		Database:     "ditto",
		User:         "ditto",
		Password:     "ditto",
		RootPassword: "ditto-root",
	}
}

func driverNameFor(engineName string) string {
	switch engineName {
	case EngineMySQL:
		return "mysql"
	default:
		return "pgx"
	}
}

func localIPv4() (string, error) {
	conn, err := net.Dial("udp", "198.51.100.1:80")
	if err == nil {
		defer func() { _ = conn.Close() }()
		if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok && addr.IP != nil && !addr.IP.IsLoopback() {
			if ip := addr.IP.To4(); ip != nil {
				return ip.String(), nil
			}
		}
	}

	ifaces, ifaceErr := net.Interfaces()
	if ifaceErr != nil {
		return "", fmt.Errorf("inspect interfaces: %w", ifaceErr)
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch value := addr.(type) {
			case *net.IPNet:
				ip = value.IP
			case *net.IPAddr:
				ip = value.IP
			}
			if ip == nil || ip.IsLoopback() {
				continue
			}
			if v4 := ip.To4(); v4 != nil {
				return v4.String(), nil
			}
		}
	}
	return "", fmt.Errorf("no non-loopback IPv4 address found")
}

func intPtr(v int) *int { return &v }
