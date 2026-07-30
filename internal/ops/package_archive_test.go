package ops

import (
	"encoding/binary"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"
)

func TestBundletoolTOCFixtureSelection(t *testing.T) {
	densityTarget, err := hex.DecodeString("0a020806")
	if err != nil {
		t.Fatal(err)
	}
	densities, _, err := parseDensityTargeting(densityTarget)
	if err != nil || len(densities) != 1 || densities[0] != 320 {
		t.Fatalf("density targeting parse: %v, %v", densities, err)
	}
	rawHex, err := os.ReadFile("testdata/toc_arm64.hex")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := hex.DecodeString(strings.TrimSpace(string(rawHex)))
	if err != nil {
		t.Fatal(err)
	}
	selection, err := selectAPKS(raw, androidDeviceSpec{
		ABIs:    []string{"arm64-v8a", "armeabi-v7a"},
		Density: 300,
		Locales: []string{"zh-CN"},
		SDK:     36,
	})
	if err != nil {
		t.Fatal(err)
	}
	if selection.PackageName != "com.example.fixture" || selection.Variant != 7 || selection.ABI != "arm64-v8a" || selection.Density != 320 {
		t.Fatalf("unexpected selection metadata: %+v", selection)
	}
	joined := strings.Join(selection.Paths, "\n")
	for _, expected := range []string{"base-master.apk", "base-arm64_v8a.apk", "base-zh.apk", "base-xhdpi.apk"} {
		if !strings.Contains(joined, expected) {
			t.Errorf("missing selected APK %s: %v", expected, selection.Paths)
		}
	}
	for _, excluded := range []string{"base-x86_64.apk", "base-en.apk", "base-xxhdpi.apk"} {
		if strings.Contains(joined, excluded) {
			t.Errorf("incompatible APK selected %s: %v", excluded, selection.Paths)
		}
	}
}

func TestXAPKManifestFixtureAndOBBGuard(t *testing.T) {
	raw, err := os.ReadFile("testdata/xapk_manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := parseXAPKManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.SplitAPKs) != 2 || len(manifest.ExpansionFiles) != 1 {
		t.Fatalf("unexpected manifest: %+v", manifest)
	}
	path, err := safeXAPKOBBPath(manifest, manifest.ExpansionFiles[0])
	if err != nil || !strings.HasSuffix(path, "main.1.com.example.fixture.obb") {
		t.Fatalf("valid OBB path rejected: %q %v", path, err)
	}
	bad := manifest.ExpansionFiles[0]
	bad.InstallPath = "/storage/emulated/0/Android/obb/other.package/evil.obb"
	if _, err := safeXAPKOBBPath(manifest, bad); err == nil {
		t.Fatal("cross-package OBB path unexpectedly accepted")
	}
}

func TestPackageSessionIDParsing(t *testing.T) {
	for _, value := range []string{
		"Success: created install session [42]",
		"Success: created install sessionId=7",
		"Success: created install session: 99",
	} {
		id, err := parsePackageSessionID(value)
		if err != nil || id <= 0 {
			t.Fatalf("parse %q: id=%d err=%v", value, id, err)
		}
	}
}

func TestNearestScheduleAndEventParsing(t *testing.T) {
	now := time.Now()
	schedules := map[string]*scheduleState{
		"later":    {Enabled: true, NextRunAt: now.Add(10 * time.Minute)},
		"first":    {Enabled: true, NextRunAt: now.Add(time.Minute)},
		"disabled": {Enabled: false, NextRunAt: now.Add(time.Second)},
		"event":    {Enabled: true, Type: "network"},
	}
	next, ok := nearestScheduleTime(schedules)
	if !ok || !next.Equal(schedules["first"].NextRunAt) {
		t.Fatalf("nearest timer = %v, %v", next, ok)
	}

	netlink := make([]byte, 16)
	binary.NativeEndian.PutUint32(netlink[:4], 16)
	binary.NativeEndian.PutUint16(netlink[4:6], 20) // RTM_NEWADDR
	if !parseNetlinkRouteEvent(netlink) {
		t.Fatal("RTM_NEWADDR event was not recognized")
	}
	uevent := []byte("change@/devices/platform/battery\x00ACTION=change\x00SUBSYSTEM=power_supply\x00POWER_SUPPLY_STATUS=Charging\x00")
	if !ueventIndicatesCharging(uevent) {
		t.Fatal("charging uevent was not recognized")
	}
	if ueventIndicatesCharging([]byte("change@/devices/wlan0\x00SUBSYSTEM=net\x00")) {
		t.Fatal("non-power uevent was misclassified")
	}
}

func FuzzParseBundletoolTOC(f *testing.F) {
	rawHex, err := os.ReadFile("testdata/toc_arm64.hex")
	if err != nil {
		f.Fatal(err)
	}
	valid, err := hex.DecodeString(strings.TrimSpace(string(rawHex)))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid)
	f.Add([]byte{0x0a, 0xff})
	f.Add([]byte{0})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		_, _, _, _ = parseBuildApksResult(data)
		_, _ = selectAPKS(data, androidDeviceSpec{
			ABIs:    []string{"arm64-v8a", "armeabi-v7a"},
			Density: 320,
			Locales: []string{"zh-CN"},
			SDK:     36,
		})
	})
}

func FuzzParseXAPKManifest(f *testing.F) {
	valid, err := os.ReadFile("testdata/xapk_manifest.json")
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid)
	f.Add([]byte(`{"package_name":"com.example","split_apks":[{"file":"../escape.apk"}]}`))
	f.Add([]byte(`{}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		manifest, err := parseXAPKManifest(data)
		if err != nil {
			return
		}
		if manifest.PackageName == "" ||
			strings.ContainsAny(manifest.PackageName, " \t\r\n/\\") {
			t.Fatalf("accepted invalid package name %q", manifest.PackageName)
		}
		for _, item := range append(
			append([]xapkFile{}, manifest.SplitAPKs...),
			manifest.ExpansionFiles...,
		) {
			if item.File == "" {
				t.Fatal("accepted empty archive path")
			}
			if _, err := safeArchivePath(".", item.File); err != nil {
				t.Fatalf("accepted unsafe archive path %q: %v", item.File, err)
			}
		}
	})
}
