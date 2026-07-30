package ops

import (
	"archive/zip"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

type protoWireField struct {
	Number int
	Type   int
	Varint uint64
	Bytes  []byte
}

type apkTarget struct {
	ABIs                 []string
	ABIAlternatives      []string
	Densities            []int
	DensityAlternatives  []int
	Languages            []string
	LanguageAlternatives []string
	MinSDKs              []int
	SDKAlternatives      []int
}

type apkDescription struct {
	Path     string
	Module   string
	Delivery int
	Target   apkTarget
	IsMaster bool
	Variant  int
}

type apksSelection struct {
	PackageName string
	Paths       []string
	Variant     int
	ABI         string
	Density     int
	Locales     []string
}

type androidDeviceSpec struct {
	ABIs    []string
	Density int
	Locales []string
	SDK     int
}

func parseProtoWire(data []byte) ([]protoWireField, error) {
	if len(data) > 64*1024*1024 {
		return nil, errors.New("protobuf 数据超过 64 MiB 上限")
	}
	fields := make([]protoWireField, 0)
	for offset := 0; offset < len(data); {
		key, used, err := readProtoVarint(data[offset:])
		if err != nil {
			return nil, fmt.Errorf("protobuf key at %d: %w", offset, err)
		}
		offset += used
		number := int(key >> 3)
		wireType := int(key & 7)
		if number <= 0 {
			return nil, errors.New("protobuf field number 无效")
		}
		field := protoWireField{Number: number, Type: wireType}
		switch wireType {
		case 0:
			field.Varint, used, err = readProtoVarint(data[offset:])
			if err != nil {
				return nil, err
			}
			offset += used
		case 1:
			if offset+8 > len(data) {
				return nil, io.ErrUnexpectedEOF
			}
			field.Bytes = append([]byte(nil), data[offset:offset+8]...)
			offset += 8
		case 2:
			length, lengthUsed, lengthErr := readProtoVarint(data[offset:])
			if lengthErr != nil {
				return nil, lengthErr
			}
			offset += lengthUsed
			if length > uint64(len(data)-offset) {
				return nil, io.ErrUnexpectedEOF
			}
			field.Bytes = append([]byte(nil), data[offset:offset+int(length)]...)
			offset += int(length)
		case 5:
			if offset+4 > len(data) {
				return nil, io.ErrUnexpectedEOF
			}
			field.Bytes = append([]byte(nil), data[offset:offset+4]...)
			offset += 4
		default:
			return nil, fmt.Errorf("不支持 protobuf wire type %d", wireType)
		}
		fields = append(fields, field)
		if len(fields) > 1000000 {
			return nil, errors.New("protobuf 字段数量超过上限")
		}
	}
	return fields, nil
}

func readProtoVarint(data []byte) (uint64, int, error) {
	var value uint64
	for index := 0; index < len(data) && index < 10; index++ {
		current := data[index]
		if index == 9 && current > 1 {
			return 0, 0, errors.New("protobuf varint 溢出")
		}
		value |= uint64(current&0x7f) << (7 * index)
		if current&0x80 == 0 {
			return value, index + 1, nil
		}
	}
	return 0, 0, io.ErrUnexpectedEOF
}

func protoMessages(fields []protoWireField, number int) [][]byte {
	out := make([][]byte, 0)
	for _, field := range fields {
		if field.Number == number && field.Type == 2 {
			out = append(out, field.Bytes)
		}
	}
	return out
}

func protoString(fields []protoWireField, number int) string {
	for _, field := range fields {
		if field.Number == number && field.Type == 2 {
			return string(field.Bytes)
		}
	}
	return ""
}

func protoInt(fields []protoWireField, number int) int {
	for _, field := range fields {
		if field.Number == number && field.Type == 0 {
			return int(field.Varint)
		}
	}
	return 0
}

func parseBuildApksResult(data []byte) (string, []apkDescription, []variantDescriptor, error) {
	root, err := parseProtoWire(data)
	if err != nil {
		return "", nil, nil, err
	}
	packageName := protoString(root, 4)
	descriptions := make([]apkDescription, 0)
	variants := make([]variantDescriptor, 0)
	for _, variantRaw := range protoMessages(root, 1) {
		variantFields, parseErr := parseProtoWire(variantRaw)
		if parseErr != nil {
			return "", nil, nil, parseErr
		}
		variant := variantDescriptor{Number: protoInt(variantFields, 3)}
		if targeting := protoMessages(variantFields, 1); len(targeting) > 0 {
			variant.Target, parseErr = parseVariantTarget(targeting[0])
			if parseErr != nil {
				return "", nil, nil, parseErr
			}
		}
		variants = append(variants, variant)
		for _, setRaw := range protoMessages(variantFields, 2) {
			setFields, setErr := parseProtoWire(setRaw)
			if setErr != nil {
				return "", nil, nil, setErr
			}
			moduleName := ""
			delivery := 0
			if metadata := protoMessages(setFields, 1); len(metadata) > 0 {
				metaFields, metaErr := parseProtoWire(metadata[0])
				if metaErr != nil {
					return "", nil, nil, metaErr
				}
				moduleName = protoString(metaFields, 1)
				delivery = protoInt(metaFields, 6)
			}
			for _, apkRaw := range protoMessages(setFields, 2) {
				apkFields, apkErr := parseProtoWire(apkRaw)
				if apkErr != nil {
					return "", nil, nil, apkErr
				}
				description := apkDescription{
					Path: protoString(apkFields, 2), Module: moduleName,
					Delivery: delivery, Variant: variant.Number,
				}
				if targeting := protoMessages(apkFields, 1); len(targeting) > 0 {
					description.Target, apkErr = parseAPKTarget(targeting[0])
					if apkErr != nil {
						return "", nil, nil, apkErr
					}
				}
				if metadata := protoMessages(apkFields, 3); len(metadata) > 0 {
					metaFields, metaErr := parseProtoWire(metadata[0])
					if metaErr != nil {
						return "", nil, nil, metaErr
					}
					description.IsMaster = protoInt(metaFields, 2) != 0
				}
				if description.Path == "" {
					return "", nil, nil, errors.New("toc.pb ApkDescription 缺少 path")
				}
				descriptions = append(descriptions, description)
			}
		}
	}
	if len(variants) == 0 || len(descriptions) == 0 {
		return "", nil, nil, errors.New("toc.pb 未包含可安装 Variant/APK")
	}
	return packageName, descriptions, variants, nil
}

type variantDescriptor struct {
	Number int
	Target apkTarget
}

func parseVariantTarget(data []byte) (apkTarget, error) {
	fields, err := parseProtoWire(data)
	if err != nil {
		return apkTarget{}, err
	}
	var target apkTarget
	if values := protoMessages(fields, 1); len(values) > 0 {
		target.MinSDKs, target.SDKAlternatives, err = parseSDKTargeting(values[0])
		if err != nil {
			return target, err
		}
	}
	if values := protoMessages(fields, 2); len(values) > 0 {
		target.ABIs, target.ABIAlternatives, err = parseABITargeting(values[0])
		if err != nil {
			return target, err
		}
	}
	if values := protoMessages(fields, 3); len(values) > 0 {
		target.Densities, target.DensityAlternatives, err = parseDensityTargeting(values[0])
	}
	return target, err
}

func parseAPKTarget(data []byte) (apkTarget, error) {
	fields, err := parseProtoWire(data)
	if err != nil {
		return apkTarget{}, err
	}
	var target apkTarget
	if values := protoMessages(fields, 1); len(values) > 0 {
		target.ABIs, target.ABIAlternatives, err = parseABITargeting(values[0])
		if err != nil {
			return target, err
		}
	}
	if values := protoMessages(fields, 3); len(values) > 0 {
		target.Languages, target.LanguageAlternatives, err = parseStringTargeting(values[0])
		if err != nil {
			return target, err
		}
	}
	if values := protoMessages(fields, 4); len(values) > 0 {
		target.Densities, target.DensityAlternatives, err = parseDensityTargeting(values[0])
		if err != nil {
			return target, err
		}
	}
	if values := protoMessages(fields, 5); len(values) > 0 {
		target.MinSDKs, target.SDKAlternatives, err = parseSDKTargeting(values[0])
	}
	return target, err
}

func parseABITargeting(data []byte) ([]string, []string, error) {
	fields, err := parseProtoWire(data)
	if err != nil {
		return nil, nil, err
	}
	parse := func(number int) ([]string, error) {
		out := make([]string, 0)
		for _, raw := range protoMessages(fields, number) {
			abi, parseErr := parseProtoWire(raw)
			if parseErr != nil {
				return nil, parseErr
			}
			alias := protoInt(abi, 1)
			name, exists := map[int]string{1: "armeabi", 2: "armeabi-v7a", 3: "arm64-v8a", 4: "x86", 5: "x86_64", 6: "mips", 7: "mips64", 8: "riscv64"}[alias]
			if exists {
				out = append(out, name)
			}
		}
		return out, nil
	}
	values, err := parse(1)
	if err != nil {
		return nil, nil, err
	}
	alternatives, err := parse(2)
	return values, alternatives, err
}

func parseDensityTargeting(data []byte) ([]int, []int, error) {
	fields, err := parseProtoWire(data)
	if err != nil {
		return nil, nil, err
	}
	parse := func(number int) ([]int, error) {
		out := make([]int, 0)
		for _, raw := range protoMessages(fields, number) {
			density, parseErr := parseProtoWire(raw)
			if parseErr != nil {
				return nil, parseErr
			}
			value := 0
			alias := protoInt(density, 1)
			if alias != 0 {
				value = map[int]int{1: 0, 2: 120, 3: 160, 4: 213, 5: 240, 6: 320, 7: 480, 8: 640}[alias]
			}
			if value == 0 {
				value = protoInt(density, 2)
			}
			if value >= 0 {
				out = append(out, value)
			}
		}
		return out, nil
	}
	values, err := parse(1)
	if err != nil {
		return nil, nil, err
	}
	alternatives, err := parse(2)
	return values, alternatives, err
}

func parseStringTargeting(data []byte) ([]string, []string, error) {
	fields, err := parseProtoWire(data)
	if err != nil {
		return nil, nil, err
	}
	values := make([]string, 0)
	alternatives := make([]string, 0)
	for _, field := range fields {
		if field.Type != 2 {
			continue
		}
		switch field.Number {
		case 1:
			values = append(values, string(field.Bytes))
		case 2:
			alternatives = append(alternatives, string(field.Bytes))
		}
	}
	return values, alternatives, nil
}

func parseSDKTargeting(data []byte) ([]int, []int, error) {
	fields, err := parseProtoWire(data)
	if err != nil {
		return nil, nil, err
	}
	parse := func(number int) ([]int, error) {
		out := make([]int, 0)
		for _, raw := range protoMessages(fields, number) {
			sdkFields, parseErr := parseProtoWire(raw)
			if parseErr != nil {
				return nil, parseErr
			}
			wrappers := protoMessages(sdkFields, 1)
			if len(wrappers) == 0 {
				continue
			}
			wrapper, parseErr := parseProtoWire(wrappers[0])
			if parseErr != nil {
				return nil, parseErr
			}
			out = append(out, protoInt(wrapper, 1))
		}
		return out, nil
	}
	values, err := parse(1)
	if err != nil {
		return nil, nil, err
	}
	alternatives, err := parse(2)
	return values, alternatives, err
}

func selectAPKS(data []byte, device androidDeviceSpec) (apksSelection, error) {
	packageName, descriptions, variants, err := parseBuildApksResult(data)
	if err != nil {
		return apksSelection{}, err
	}
	sort.Slice(variants, func(i, j int) bool { return variants[i].Number > variants[j].Number })
	selectedVariant := -1
	for _, variant := range variants {
		if targetMatchesVariant(variant.Target, device) {
			selectedVariant = variant.Number
			break
		}
	}
	if selectedVariant < 0 {
		return apksSelection{}, errors.New("toc.pb 中没有匹配设备 API/ABI/密度的 Variant")
	}
	candidates := make([]apkDescription, 0)
	for _, description := range descriptions {
		if description.Variant == selectedVariant && (description.Delivery == 0 || description.Delivery == 1) {
			candidates = append(candidates, description)
		}
	}
	chosenABI := ""
	for _, deviceABI := range device.ABIs {
		for _, description := range candidates {
			if containsStringFold(description.Target.ABIs, deviceABI) {
				chosenABI = deviceABI
				break
			}
		}
		if chosenABI != "" {
			break
		}
	}
	densities := make([]int, 0)
	sdkMins := make([]int, 0)
	for _, description := range candidates {
		densities = append(densities, description.Target.Densities...)
		sdkMins = append(sdkMins, description.Target.MinSDKs...)
	}
	chosenDensity := nearestDensity(device.Density, densities)
	chosenSDK := highestNotAbove(device.SDK, sdkMins)
	selected := make([]string, 0)
	for _, description := range candidates {
		target := description.Target
		include := description.IsMaster || targetIsEmpty(target)
		if !include {
			include = true
			if len(target.ABIs) > 0 && (chosenABI == "" || !containsStringFold(target.ABIs, chosenABI)) {
				include = false
			}
			if len(target.Densities) > 0 && (chosenDensity == 0 || !containsInt(target.Densities, chosenDensity)) {
				include = false
			}
			if len(target.MinSDKs) > 0 && (chosenSDK == 0 || !containsInt(target.MinSDKs, chosenSDK)) {
				include = false
			}
			if len(target.Languages) > 0 && !languagesIntersect(target.Languages, device.Locales) {
				include = false
			}
			if len(target.ABIs) == 0 && len(target.ABIAlternatives) > 0 && containsAnyStringFold(target.ABIAlternatives, device.ABIs) {
				include = false
			}
			if len(target.Languages) == 0 && len(target.LanguageAlternatives) > 0 && languagesIntersect(target.LanguageAlternatives, device.Locales) {
				include = false
			}
		}
		if include {
			if _, err := safeArchivePath(".", description.Path); err != nil {
				return apksSelection{}, err
			}
			selected = append(selected, description.Path)
		}
	}
	if len(selected) == 0 {
		return apksSelection{}, errors.New("toc.pb 解析成功但未选择到 APK")
	}
	sort.Strings(selected)
	return apksSelection{PackageName: packageName, Paths: selected, Variant: selectedVariant, ABI: chosenABI, Density: chosenDensity, Locales: device.Locales}, nil
}

func targetMatchesVariant(target apkTarget, device androidDeviceSpec) bool {
	if len(target.ABIs) > 0 && !containsAnyStringFold(target.ABIs, device.ABIs) {
		return false
	}
	if len(target.ABIs) == 0 && len(target.ABIAlternatives) > 0 && containsAnyStringFold(target.ABIAlternatives, device.ABIs) {
		return false
	}
	if len(target.MinSDKs) > 0 && highestNotAbove(device.SDK, target.MinSDKs) == 0 {
		return false
	}
	if len(target.Densities) > 0 && nearestDensity(device.Density, target.Densities) == 0 {
		return false
	}
	return true
}

func targetIsEmpty(target apkTarget) bool {
	return len(target.ABIs) == 0 && len(target.Densities) == 0 && len(target.Languages) == 0 && len(target.MinSDKs) == 0 &&
		len(target.ABIAlternatives) == 0 && len(target.DensityAlternatives) == 0 && len(target.LanguageAlternatives) == 0 && len(target.SDKAlternatives) == 0
}

func containsStringFold(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(value, wanted) {
			return true
		}
	}
	return false
}

func containsAnyStringFold(left, right []string) bool {
	for _, value := range left {
		if containsStringFold(right, value) {
			return true
		}
	}
	return false
}

func containsInt(values []int, wanted int) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func languagesIntersect(targets, locales []string) bool {
	for _, target := range targets {
		target = strings.ToLower(strings.ReplaceAll(target, "_", "-"))
		for _, locale := range locales {
			locale = strings.ToLower(strings.ReplaceAll(locale, "_", "-"))
			if locale == target || strings.Split(locale, "-")[0] == strings.Split(target, "-")[0] {
				return true
			}
		}
	}
	return false
}

func nearestDensity(wanted int, values []int) int {
	best := 0
	bestDistance := int(^uint(0) >> 1)
	for _, value := range values {
		distance := value - wanted
		if distance < 0 {
			distance = -distance
		}
		if distance < bestDistance || distance == bestDistance && value > best {
			best, bestDistance = value, distance
		}
	}
	return best
}

func highestNotAbove(wanted int, values []int) int {
	best := 0
	for _, value := range values {
		if value <= wanted && value > best {
			best = value
		}
	}
	return best
}

func readZipEntry(archive, name string, maxBytes int64) ([]byte, error) {
	reader, err := zip.OpenReader(archive)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	for _, item := range reader.File {
		if item.Name != name {
			continue
		}
		if item.UncompressedSize64 > uint64(maxBytes) {
			return nil, fmt.Errorf("%s 超过大小上限", name)
		}
		input, err := item.Open()
		if err != nil {
			return nil, err
		}
		data, readErr := io.ReadAll(io.LimitReader(input, maxBytes+1))
		closeErr := input.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if int64(len(data)) > maxBytes {
			return nil, fmt.Errorf("%s 超过大小上限", name)
		}
		return data, nil
	}
	return nil, os.ErrNotExist
}

func extractZipSelection(archive string, names []string, destination string) ([]string, error) {
	reader, err := zip.OpenReader(archive)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	byName := make(map[string]*zip.File, len(reader.File))
	for _, item := range reader.File {
		byName[item.Name] = item
	}
	if err := os.MkdirAll(destination, 0o750); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(names))
	used := make(map[string]int)
	for _, name := range names {
		if _, err := safeArchivePath(".", name); err != nil {
			return nil, err
		}
		item := byName[name]
		if item == nil || item.FileInfo().IsDir() {
			return nil, fmt.Errorf("压缩包缺少清单文件 %s", name)
		}
		base := filepath.Base(filepath.FromSlash(name))
		if count := used[base]; count > 0 {
			base = strings.TrimSuffix(base, filepath.Ext(base)) + "-" + strconv.Itoa(count) + filepath.Ext(base)
		}
		used[filepath.Base(filepath.FromSlash(name))]++
		target := filepath.Join(destination, base)
		input, err := item.Open()
		if err != nil {
			return nil, err
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
		if err != nil {
			_ = input.Close()
			return nil, err
		}
		_, copyErr := io.CopyBuffer(output, input, make([]byte, 256*1024))
		closeOutErr := output.Close()
		closeInErr := input.Close()
		for _, candidate := range []error{copyErr, closeOutErr, closeInErr} {
			if candidate != nil {
				return nil, candidate
			}
		}
		out = append(out, target)
	}
	return out, nil
}

func (m *Manager) currentAndroidDeviceSpec(ctx context.Context) androidDeviceSpec {
	abis := strings.Split(strings.TrimSpace(getProp(ctx, m, "ro.product.cpu.abilist").Stdout), ",")
	if len(abis) == 0 || abis[0] == "" {
		abis = []string{runtimeArchABI()}
	}
	density, _ := strconv.Atoi(strings.TrimSpace(getProp(ctx, m, "ro.sf.lcd_density").Stdout))
	if density == 0 {
		result := m.runAndroid(ctx, commandVariant{Name: "wm", Args: []string{"density"}, Strategy: "wm_density"})
		density = parseLastInteger(result.Stdout)
	}
	locale := strings.TrimSpace(getProp(ctx, m, "persist.sys.locale").Stdout)
	if locale == "" {
		locale = strings.TrimSpace(getProp(ctx, m, "ro.product.locale").Stdout)
	}
	locales := []string{}
	for _, item := range strings.FieldsFunc(locale, func(r rune) bool { return r == ',' || r == ':' }) {
		if strings.TrimSpace(item) != "" {
			locales = append(locales, strings.TrimSpace(item))
		}
	}
	return androidDeviceSpec{ABIs: abis, Density: density, Locales: locales, SDK: androidAPILevel(ctx, m)}
}

func runtimeArchABI() string {
	switch runtime.GOARCH {
	case "arm64":
		return "arm64-v8a"
	case "arm":
		return "armeabi-v7a"
	case "amd64":
		return "x86_64"
	case "386":
		return "x86"
	default:
		return runtime.GOARCH
	}
}

func parseLastInteger(value string) int {
	matches := regexp.MustCompile(`\d+`).FindAllString(value, -1)
	if len(matches) == 0 {
		return 0
	}
	parsed, _ := strconv.Atoi(matches[len(matches)-1])
	return parsed
}

type xapkManifest struct {
	PackageName    string     `json:"package_name"`
	SplitAPKs      []xapkFile `json:"split_apks"`
	APKs           []xapkFile `json:"apks"`
	ExpansionFiles []xapkFile `json:"expansion_files"`
	Expansions     []xapkFile `json:"expansions"`
}

type xapkFile struct {
	File        string `json:"file"`
	ID          string `json:"id"`
	InstallPath string `json:"install_path"`
}

func parseXAPKManifest(data []byte) (xapkManifest, error) {
	var manifest xapkManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return manifest, err
	}
	if manifest.PackageName == "" || strings.ContainsAny(manifest.PackageName, " \t\r\n/\\") {
		return manifest, errors.New("XAPK manifest package_name 无效")
	}
	manifest.SplitAPKs = append(manifest.SplitAPKs, manifest.APKs...)
	manifest.ExpansionFiles = append(manifest.ExpansionFiles, manifest.Expansions...)
	if len(manifest.SplitAPKs) == 0 {
		return manifest, errors.New("XAPK manifest 未列出 split_apks/apks")
	}
	for _, item := range append(append([]xapkFile{}, manifest.SplitAPKs...), manifest.ExpansionFiles...) {
		if _, err := safeArchivePath(".", item.File); err != nil || item.File == "" {
			return manifest, fmt.Errorf("XAPK manifest 文件路径无效：%s", item.File)
		}
	}
	return manifest, nil
}

func safeXAPKOBBPath(manifest xapkManifest, item xapkFile) (string, error) {
	base := filepath.Join("/storage/emulated/0/Android/obb", manifest.PackageName)
	install := strings.TrimSpace(item.InstallPath)
	if install == "" {
		install = filepath.Join(base, filepath.Base(filepath.FromSlash(item.File)))
	}
	install = strings.Replace(install, "/sdcard/", "/storage/emulated/0/", 1)
	install = filepath.Clean(install)
	relative, err := filepath.Rel(base, install)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("OBB install_path 超出目标包目录：%s", item.InstallPath)
	}
	return install, nil
}

func copyVerified(source, destination string) error {
	if err := copyPath(source, destination, true); err != nil {
		return err
	}
	sourceHash, err := hashFileSHA256(source)
	if err != nil {
		return err
	}
	destHash, err := hashFileSHA256(destination)
	if err != nil {
		return err
	}
	if sourceHash != destHash {
		return errors.New("复制后 SHA-256 不一致")
	}
	return nil
}

func encodeProtoVarint(value uint64) []byte {
	var buffer [10]byte
	count := binary.PutUvarint(buffer[:], value)
	return append([]byte(nil), buffer[:count]...)
}
