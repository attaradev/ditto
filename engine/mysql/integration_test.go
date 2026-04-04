//go:build integration

package mysql_test

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/attaradev/ditto/engine"
	"github.com/attaradev/ditto/internal/dockerutil"
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/go-connections/nat"
	"github.com/oklog/ulid/v2"
	_ "github.com/go-sql-driver/mysql"
)

func TestMySQLDumpRestoreCycle(t *testing.T) {
	ctx := t.Context()
	eng := getMySQLEngine(t)
	cli := newDockerClient(t, ctx)

	id := ulid.Make().String()
	netName := "ditto-my-test-" + id
	srcName := "ditto-my-src-" + id
	copyName := "ditto-my-copy-" + id

	netID := createNetwork(t, ctx, cli, netName)
	t.Cleanup(func() { _ = cli.NetworkRemove(context.Background(), netID) })

	srcPort := mustFreePort(t)
	copyPort := mustFreePort(t)

	if err := dockerutil.EnsureImage(ctx, cli, "mysql:8.4"); err != nil {
		t.Fatalf("pull image: %v", err)
	}

	srcID := startMySQLContainer(t, ctx, cli, srcName, netName, srcPort, "src", "src", "srcdb")
	t.Cleanup(func() { stopRemove(cli, srcName, srcID) })

	srcDSN := fmt.Sprintf("src:src@tcp(localhost:%d)/srcdb", srcPort)
	if err := waitMySQLReady(ctx, srcDSN, 3*time.Minute); err != nil {
		t.Fatalf("source not ready: %v", err)
	}
	seedMySQLWidgets(t, ctx, srcDSN)

	dumpDir := t.TempDir()
	dumpPath := dumpDir + "/dump.sql.gz"
	src := engine.SourceConfig{
		Host:        srcName, // container name used as DNS alias within netName
		Port:        3306,
		Database:    "srcdb",
		User:        "src",
		Password:    "src",
		NetworkName: netName, // attach dump helper to same network so DNS resolves
	}
	if err := eng.Dump(ctx, cli, "", src, dumpPath, engine.DumpOptions{}); err != nil {
		t.Fatalf("Dump: %v", err)
	}

	copyID := startMySQLContainer(t, ctx, cli, copyName, netName, copyPort, "ditto", "ditto", "ditto")
	t.Cleanup(func() { stopRemove(cli, copyName, copyID) })

	if err := eng.WaitReady(copyPort, 3*time.Minute); err != nil {
		t.Fatalf("copy not ready: %v", err)
	}
	if err := eng.Restore(ctx, cli, dumpPath, copyName); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	copyDSN := fmt.Sprintf("ditto:ditto@tcp(localhost:%d)/ditto", copyPort)
	assertMySQLWidgetCount(t, ctx, copyDSN, 2)
}

func TestMySQLSchemaOnlyDump(t *testing.T) {
	ctx := t.Context()
	eng := getMySQLEngine(t)
	cli := newDockerClient(t, ctx)

	id := ulid.Make().String()
	netName := "ditto-my-schema-" + id
	srcName := "ditto-my-schema-src-" + id
	copyName := "ditto-my-schema-copy-" + id

	netID := createNetwork(t, ctx, cli, netName)
	t.Cleanup(func() { _ = cli.NetworkRemove(context.Background(), netID) })

	srcPort := mustFreePort(t)
	copyPort := mustFreePort(t)

	if err := dockerutil.EnsureImage(ctx, cli, "mysql:8.4"); err != nil {
		t.Fatalf("pull image: %v", err)
	}

	srcID := startMySQLContainer(t, ctx, cli, srcName, netName, srcPort, "src", "src", "srcdb")
	t.Cleanup(func() { stopRemove(cli, srcName, srcID) })

	srcDSN := fmt.Sprintf("src:src@tcp(localhost:%d)/srcdb", srcPort)
	if err := waitMySQLReady(ctx, srcDSN, 3*time.Minute); err != nil {
		t.Fatalf("source not ready: %v", err)
	}
	seedMySQLWidgets(t, ctx, srcDSN)

	dumpDir := t.TempDir()
	dumpPath := dumpDir + "/schema.sql.gz"
	src := engine.SourceConfig{
		Host:        srcName,
		Port:        3306,
		Database:    "srcdb",
		User:        "src",
		Password:    "src",
		NetworkName: netName,
	}
	if err := eng.Dump(ctx, cli, "", src, dumpPath, engine.DumpOptions{SchemaOnly: true}); err != nil {
		t.Fatalf("Dump schema-only: %v", err)
	}

	copyID := startMySQLContainer(t, ctx, cli, copyName, netName, copyPort, "ditto", "ditto", "ditto")
	t.Cleanup(func() { stopRemove(cli, copyName, copyID) })

	if err := eng.WaitReady(copyPort, 3*time.Minute); err != nil {
		t.Fatalf("copy not ready: %v", err)
	}
	if err := eng.Restore(ctx, cli, dumpPath, copyName); err != nil {
		t.Fatalf("Restore schema-only: %v", err)
	}

	// Table must exist but contain zero rows.
	copyDSN := fmt.Sprintf("ditto:ditto@tcp(localhost:%d)/ditto", copyPort)
	assertMySQLWidgetCount(t, ctx, copyDSN, 0)
}

// --- helpers ---

func getMySQLEngine(t *testing.T) engine.Engine {
	t.Helper()
	eng, err := engine.Get("mysql")
	if err != nil {
		t.Fatalf("engine.Get(mysql): %v", err)
	}
	return eng
}

func newDockerClient(t *testing.T, ctx context.Context) *dockerclient.Client {
	t.Helper()
	cli, _, err := dockerutil.NewClient(ctx, "")
	if err != nil {
		t.Skipf("Docker not available: %v", err)
	}
	return cli
}

func createNetwork(t *testing.T, ctx context.Context, cli *dockerclient.Client, name string) string {
	t.Helper()
	resp, err := cli.NetworkCreate(ctx, name, network.CreateOptions{})
	if err != nil {
		t.Fatalf("NetworkCreate %s: %v", name, err)
	}
	return resp.ID
}

func startMySQLContainer(
	t *testing.T,
	ctx context.Context,
	cli *dockerclient.Client,
	name, netName string,
	hostPort int,
	user, password, dbname string,
) string {
	t.Helper()
	portStr := fmt.Sprintf("%d", hostPort)
	exposed := nat.Port("3306/tcp")

	resp, err := cli.ContainerCreate(ctx,
		&container.Config{
			Image: "mysql:8.4",
			Env: []string{
				"MYSQL_USER=" + user,
				"MYSQL_PASSWORD=" + password,
				"MYSQL_DATABASE=" + dbname,
				"MYSQL_ROOT_PASSWORD=" + user + "-root",
			},
			ExposedPorts: nat.PortSet{exposed: struct{}{}},
		},
		&container.HostConfig{
			PortBindings: nat.PortMap{
				exposed: []nat.PortBinding{{HostIP: "127.0.0.1", HostPort: portStr}},
			},
			Mounts: []mount.Mount{},
		},
		&network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				netName: {Aliases: []string{name}},
			},
		},
		nil, name,
	)
	if err != nil {
		t.Fatalf("ContainerCreate %s: %v", name, err)
	}
	if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		t.Fatalf("ContainerStart %s: %v", name, err)
	}
	return resp.ID
}

func stopRemove(cli *dockerclient.Client, name, id string) {
	bg := context.Background()
	_ = cli.ContainerStop(bg, name, container.StopOptions{Timeout: intPtr(5)})
	_ = cli.ContainerRemove(bg, id, container.RemoveOptions{Force: true})
}

func waitMySQLReady(ctx context.Context, dsn string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer func() { _ = db.Close() }()

	for time.Now().Before(deadline) {
		qctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, err := db.ExecContext(qctx, "SELECT 1")
		cancel()
		if err == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("mysql not ready after %s", timeout)
}

func seedMySQLWidgets(t *testing.T, ctx context.Context, dsn string) {
	t.Helper()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open source db: %v", err)
	}
	defer func() { _ = db.Close() }()

	if _, err := db.ExecContext(ctx, `CREATE TABLE widgets (id INT AUTO_INCREMENT PRIMARY KEY, name VARCHAR(255) NOT NULL)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO widgets (name) VALUES ('foo'), ('bar')`); err != nil {
		t.Fatalf("insert rows: %v", err)
	}
}

func assertMySQLWidgetCount(t *testing.T, ctx context.Context, dsn string, want int) {
	t.Helper()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open copy db: %v", err)
	}
	defer func() { _ = db.Close() }()

	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM widgets").Scan(&count); err != nil {
		t.Fatalf("count widgets: %v", err)
	}
	if count != want {
		t.Errorf("widget count: got %d, want %d", count, want)
	}
}

func mustFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("find free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

func intPtr(i int) *int { return &i }
