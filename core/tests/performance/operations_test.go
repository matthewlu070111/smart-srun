// These CPU/allocation benchmarks compare build options on identical synthetic
// inputs. They do not stand in for device RSS, network latency or campus tests.
package performance

import (
	"fmt"
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/config"
	"github.com/matthewlu070111/smart-srun/core/internal/control"
	"github.com/matthewlu070111/smart-srun/core/internal/domain"
	"github.com/matthewlu070111/smart-srun/core/internal/portal"
	"github.com/matthewlu070111/smart-srun/core/internal/protocol/srun"
)

func BenchmarkConfigAccounts(b *testing.B) {
	for _, count := range []int{1, 4, 8} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			cfg := config.Defaults()
			for i := 0; i < count; i++ {
				cfg.CampusAccounts = append(cfg.CampusAccounts, domain.CampusAccount{
					ID: fmt.Sprintf("c%d", i), Label: "synthetic account", UserID: "test-student", Password: "synthetic-password",
					AccessMode: domain.AccessModeWired, WiredIface: fmt.Sprintf("wan%d", i), BaseURL: "http://192.0.2.1", ACID: "1"})
			}
			cfg = config.Normalize(cfg)
			data, err := config.Marshal(cfg)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				value, err := config.Parse(data)
				if err != nil || len(value.CampusAccounts) != count {
					b.Fatal("invalid fixture", err)
				}
			}
		})
	}
}

func BenchmarkLoginEncoding(b *testing.B) {
	info := srun.Info{Username: "test-student@stu", Password: "synthetic-password", IP: "192.0.2.2", ACID: "1", EncVer: "srun_bx1"}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		encoded, err := srun.EncodeInfo(info)
		if err != nil {
			b.Fatal(err)
		}
		blob := srun.EncryptedInfo("SRBX1", encoded, "0123456789abcdef0123456789abcdef", nil)
		if len(srun.Checksum("0123456789abcdef0123456789abcdef", info.Username, "synthetic-digest", "1", info.IP, "200", "1", blob)) != 40 {
			b.Fatal("invalid checksum")
		}
	}
}

func BenchmarkPortalOptions(b *testing.B) {
	data := []byte("<html>" + strings.Repeat("<p>synthetic portal information</p>", 512) + `<select name="realm"><option value="@stu">学生</option><option value="">校园网</option></select></html>`)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		options, err := portal.ReadOperators(data, "text/html; charset=UTF-8")
		if err != nil || len(options) != 2 {
			b.Fatal("invalid fixture", err)
		}
	}
}

func BenchmarkStatusRequestDecode(b *testing.B) {
	data := []byte(`{"rpc_version":1,"request_id":"synthetic","method":"status","params":{}}`)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := control.DecodeRequest(data); err != nil {
			b.Fatal(err)
		}
	}
}
