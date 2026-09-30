package update

import (
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/matthewlu070111/smart-srun/core/internal/domain"
)

func TestCheckExplainsIncompatibleNewerReleaseWithoutPlanningInstall(t *testing.T) {
	manifest := fixtureManifest("opkg", "bundle")
	version, _ := ParseVersion(manifest.Release)
	inventory := fixtureInventory("opkg", "bundle")
	inventory.FirmwareFamily = "25.12"
	source := &fixtureSource{candidates: []Version{version}, manifests: map[Version]Manifest{version: manifest}}
	candidate, result, err := Check(t.Context(), source, inventory, "rc")
	if err != nil || !result.OK || result.UpdateAvailable || candidate.Plan.ID != "" || result.PlanID != "" ||
		result.LatestVersion != manifest.Release || result.Code != domain.CodePackageIncompatible || !strings.Contains(result.Message, "25.12") {
		t.Fatalf("candidate=%+v result=%+v error=%v", candidate, result, err)
	}
}

type failedReleaseReader struct{ err error }

func (r failedReleaseReader) Read([]byte) (int, error) { return 0, r.err }

func TestReleaseBodyFailureKeepsNetworkCategoryAndRemovesPartialDownload(t *testing.T) {
	source := testSource(t, func(request *http.Request) (*http.Response, error) {
		r := response(request, "", http.StatusOK)
		r.Body = io.NopCloser(failedReleaseReader{&net.DNSError{Err: "synthetic-secret"}})
		return r, nil
	})
	_, err := source.Manifest(t.Context(), Version{Major: 2, RC: 10})
	if code, _ := ErrorStatus(err); code != domain.CodeDNSFailure {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "partial.ipk")
	err = source.Download(t.Context(), fixtureManifest("opkg", "bundle").Assets[0], path)
	if code, _ := ErrorStatus(err); code != domain.CodeDNSFailure {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("partial payload retained")
	}
}

func TestCheckExplainsMissingManifestAndLeavesNoUpdateDistinct(t *testing.T) {
	source := &fixtureSource{candidates: []Version{{Major: 2, RC: 10}}}
	_, result, err := Check(t.Context(), source, fixtureInventory("opkg", "bundle"), "rc")
	if err != nil || result.Code != domain.CodePackageIncompatible || result.LatestVersion != "2.0.0rc10" || !strings.Contains(result.Message, "发布清单") {
		t.Fatalf("%+v %v", result, err)
	}
	source.candidates = nil
	_, result, err = Check(t.Context(), source, fixtureInventory("opkg", "bundle"), "rc")
	if err != nil || !result.OK || result.Code != "" || result.LatestVersion != "" || result.UpdateAvailable {
		t.Fatalf("%+v %v", result, err)
	}
}

func TestCheckStillSelectsCompatibleFallbackAndExactRecovery(t *testing.T) {
	manifest := fixtureManifest("opkg", "bundle")
	version, _ := ParseVersion(manifest.Release)
	inventory := fixtureInventory("opkg", "bundle")
	current, _ := ParseVersion(inventory.DisplayVersion)
	recovery := fixtureManifest("opkg", "bundle")
	recovery.Release = current.String()
	recovery.Assets[0].PackageVersion = inventory.Packages["luci-app-smart-srun-bundle"]
	recovery.Assets[0].URL = strings.ReplaceAll(recovery.Assets[0].URL, manifest.Release, recovery.Release)
	for _, missing := range []bool{true, false} {
		newer := Version{Major: 2, RC: 11}
		source := &fixtureSource{candidates: []Version{newer, version},
			manifests: map[Version]Manifest{version: manifest, current: recovery}}
		if !missing {
			incompatible := fixtureManifest("opkg", "bundle")
			incompatible.Release = newer.String()
			incompatible.Assets[0].PackageVersion = "2.0.0~rc11-r1"
			incompatible.Assets[0].URL = strings.ReplaceAll(incompatible.Assets[0].URL, manifest.Release, incompatible.Release)
			incompatible.Assets[0].FirmwareCompat = []string{"23.05"}
			source.manifests[newer] = incompatible
		}
		candidate, result, err := Check(t.Context(), source, inventory, "rc")
		if err != nil || !result.OK || !result.UpdateAvailable || result.Code != "" || result.LatestVersion != manifest.Release ||
			candidate.Recovery.Release != current.String() || candidate.Plan.ID == "" {
			t.Fatalf("missing=%t candidate=%+v result=%+v error=%v", missing, candidate, result, err)
		}
	}
}

func TestReleaseDownloadClassifiesNetworkErrorsWithoutExposingWrappedText(t *testing.T) {
	for _, test := range []struct {
		err  error
		code domain.ErrorCode
	}{
		{&net.DNSError{Err: "synthetic-secret", IsTimeout: true}, domain.CodeDNSFailure},
		{x509.UnknownAuthorityError{}, domain.CodeTLSFailure},
	} {
		source := testSource(t, func(*http.Request) (*http.Response, error) { return nil, test.err })
		_, err := source.Candidates(t.Context(), Version{Major: 2, RC: 1}, "rc")
		code, message := ErrorStatus(err)
		if code != test.code || strings.Contains(message, "synthetic-secret") {
			t.Fatalf("code=%s message=%s", code, message)
		}
	}
}
