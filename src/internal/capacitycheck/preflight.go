// Package capacitycheck contains opt-in, local-only acceptance fixture checks.
// Production packages do not import it.
package capacitycheck

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"testing"

	"golang.org/x/sys/unix"
)

func Run(t *testing.T) {
	t.Helper()
	if os.Getenv("GOOMNI_CAPACITY_PREFLIGHT") != "1" {
		t.Skip("resource preflight is run only by the isolated capacity runner")
	}
	if runtime.GOOS != "linux" || os.Getuid() != 65532 {
		t.Fatal("preflight requires the isolated Linux nonroot runtime")
	}
	budget, err := strconv.ParseInt(os.Getenv("GOOMNI_SOAK_MAX_DISK_BYTES"), 10, 64)
	if err != nil || budget < 1<<30 || budget > 1<<40 {
		t.Fatal("invalid disk budget")
	}
	root := os.Getenv("TMPDIR")
	if root != "/app/storages" {
		t.Fatal("preflight requires the isolated data volume")
	}
	var stat unix.Statfs_t
	if err = unix.Statfs(root, &stat); err != nil {
		t.Fatal("cannot inspect data volume space")
	}
	free := int64(stat.Bavail) * int64(stat.Bsize)
	if free < budget+2*(1<<30) {
		report, _ := json.Marshal(map[string]any{"data_volume_free_bytes": free, "required_bytes": budget + 2*(1<<30)})
		fmt.Println("GOOMNI_CAPACITY_PREFLIGHT_BLOCKED " + string(report))
		t.Fatal("insufficient data volume free space for disk budget plus 2 GiB reserve")
	}
	f, err := os.CreateTemp(root, ".capacity-preflight-")
	if err != nil {
		t.Fatal("data volume is not writable by the nonroot runtime")
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write([]byte("synthetic resource preflight\n")); err != nil {
		f.Close()
		t.Fatal("data volume write failed")
	}
	if err = f.Sync(); err != nil {
		f.Close()
		t.Fatal("data volume sync failed")
	}
	if err = f.Close(); err != nil {
		t.Fatal("data volume close failed")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal("cannot inspect acceptance executable")
	}
	binary, err := os.Open(executable)
	if err != nil {
		t.Fatal("cannot hash acceptance executable")
	}
	defer binary.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, binary); err != nil {
		t.Fatal("cannot hash acceptance executable")
	}
	report, err := json.Marshal(map[string]any{"binary_sha256": hex.EncodeToString(hash.Sum(nil)), "architecture": runtime.GOARCH, "data_volume_free_bytes": free, "required_bytes": budget + 2*(1<<30), "uid": os.Getuid()})
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("GOOMNI_CAPACITY_PREFLIGHT " + string(report))
}
