package ops

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func (m *Manager) appOperation(ctx context.Context, req Request) Result {
	if result := requireAndroid(); result != nil {
		return *result
	}
	switch req.Tool {
	case "zcr521_app_list":
		return m.appList(ctx, req)
	case "zcr521_app_info":
		return m.appInfo(ctx, req)
	case "zcr521_app_install":
		return m.appInstall(ctx, req)
	case "zcr521_app_manage":
		return m.appManage(ctx, req)
	case "zcr521_app_permission":
		return m.appPermission(ctx, req)
	case "zcr521_app_export":
		return m.appExport(ctx, req)
	default:
		return fail("UNKNOWN_TOOL", "未知应用工具", errors.New(req.Tool), "dispatcher")
	}
}

func (m *Manager) appList(ctx context.Context, req Request) Result {
	action, err := actionOf(req, "all")
	if err != nil {
		return invalid(err.Error())
	}
	flag := ""
	switch action {
	case "all":
	case "system":
		flag = "-s"
	case "user":
		flag = "-3"
	default:
		return invalidAction(req.Tool, action, "all", "system", "user")
	}
	user, err := androidUser(req.Args)
	if err != nil {
		return invalid(err.Error())
	}
	args := []string{"list", "packages", "-f", "-U"}
	if flag != "" {
		args = append(args, flag)
	}
	if user != "current" {
		args = append(args, "--user", user)
	}
	result := m.runAndroid(ctx,
		commandVariant{Name: "pm", Args: args, Strategy: "pm_list_packages"},
		commandVariant{Name: "cmd", Args: append([]string{"package"}, args...), Strategy: "cmd_package_list"},
	)
	if !result.Success {
		return result
	}
	packages := parsePackageList(result.Stdout)
	result.Message = "应用列表读取成功"
	result.Data = map[string]any{"scope": action, "user": user, "count": len(packages), "packages": packages}
	return result
}

func parsePackageList(output string) []map[string]any {
	items := make([]map[string]any, 0)
	scanner := bufio.NewScanner(strings.NewReader(output))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "package:") {
			continue
		}
		line = strings.TrimPrefix(line, "package:")
		item := map[string]any{}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		pathPackage := fields[0]
		if index := strings.LastIndex(pathPackage, "="); index >= 0 {
			item["apkPath"] = pathPackage[:index]
			item["package"] = pathPackage[index+1:]
		} else {
			item["package"] = pathPackage
		}
		for _, field := range fields[1:] {
			if strings.HasPrefix(field, "uid:") {
				item["uid"] = strings.TrimPrefix(field, "uid:")
			}
			if strings.HasPrefix(field, "versionCode:") {
				item["versionCode"] = strings.TrimPrefix(field, "versionCode:")
			}
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		return fmt.Sprint(items[i]["package"]) < fmt.Sprint(items[j]["package"])
	})
	return items
}

func (m *Manager) appInfo(ctx context.Context, req Request) Result {
	action, err := actionOf(req, "info")
	if err != nil {
		return invalid(err.Error())
	}
	pkg, err := packageName(req.Args)
	if err != nil {
		return invalid(err.Error())
	}
	user, err := androidUser(req.Args)
	if err != nil {
		return invalid(err.Error())
	}
	switch action {
	case "info", "permissions", "components":
		result := m.runAndroid(ctx,
			commandVariant{Name: "dumpsys", Args: []string{"package", pkg}, Strategy: "dumpsys_package"},
			commandVariant{Name: "cmd", Args: []string{"package", "dump", pkg}, Strategy: "cmd_package_dump"},
		)
		if !result.Success {
			return result
		}
		if strings.TrimSpace(result.Stdout) == "" || strings.Contains(result.Stdout, "Unable to find package") {
			return fail("NOT_FOUND", "应用不存在", os.ErrNotExist, result.Strategy)
		}
		result.Message = "应用详情读取成功"
		result.Data = map[string]any{"package": pkg, "user": user, "raw": result.Stdout, "view": action}
		return result
	case "path":
		args := []string{"path"}
		if user != "current" {
			args = append(args, "--user", user)
		}
		args = append(args, pkg)
		result := m.runAndroid(ctx, commandVariant{Name: "pm", Args: args, Strategy: "pm_path"})
		if !result.Success || !strings.Contains(result.Stdout, "package:") {
			if result.Success {
				return fail("NOT_FOUND", "应用 APK 路径不存在", os.ErrNotExist, "pm_path")
			}
			return result
		}
		paths := parsePMPaths(result.Stdout)
		result.Message = "应用 APK 路径读取成功"
		result.Data = map[string]any{"package": pkg, "user": user, "paths": paths}
		return result
	default:
		return invalidAction(req.Tool, action, "components", "info", "path", "permissions")
	}
}

func parsePMPaths(output string) []string {
	paths := make([]string, 0)
	for _, line := range strings.Split(output, "\n") {
		if value := strings.TrimSpace(strings.TrimPrefix(line, "package:")); value != "" && strings.HasPrefix(strings.TrimSpace(line), "package:") {
			paths = append(paths, value)
		}
	}
	return paths
}

func (m *Manager) appInstall(ctx context.Context, req Request) Result {
	if result := requireAndroidRoot(); result != nil {
		return *result
	}
	action, err := actionOf(req, "install")
	if err != nil {
		return invalid(err.Error())
	}
	user, err := androidUser(req.Args)
	if err != nil || user == "all" {
		return invalid("安装 user 必须是 current 或单个用户 ID")
	}
	if action == "session" {
		return m.appInstallSessionAction(ctx, req, user)
	}
	var paths []string
	packageForVerify, _ := argOptionalString(req.Args, "", "package", "packageName")
	type pendingOBB struct {
		Source      string
		Destination string
	}
	pendingOBBs := make([]pendingOBB, 0)
	var selectionData any
	switch action {
	case "apk", "install", "replace", "downgrade":
		value, parseErr := argString(req.Args, "path", "apk")
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		path, resolveErr := m.resolvePath(value)
		if resolveErr != nil {
			return invalid(resolveErr.Error())
		}
		paths = []string{path}
	case "split", "install_split", "multiple":
		values, parseErr := argStringSlice(req.Args, "paths", "apks")
		if parseErr != nil || len(values) == 0 {
			return invalid("paths/apks 必须是非空数组")
		}
		for _, value := range values {
			path, resolveErr := m.resolvePath(value)
			if resolveErr != nil {
				return invalid(resolveErr.Error())
			}
			paths = append(paths, path)
		}
	case "apks":
		value, parseErr := argString(req.Args, "path", "archive")
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		archive, resolveErr := m.resolvePath(value)
		if resolveErr != nil {
			return invalid(resolveErr.Error())
		}
		if err := m.ensureRuntimeDirs(); err != nil {
			return fileFailure("安装状态目录不可用", err, "apks_selection")
		}
		temp, tempErr := os.MkdirTemp(m.cfg.StateDir, "package-install-*")
		if tempErr != nil {
			return fileFailure("安装临时目录创建失败", tempErr, "zip_package")
		}
		defer os.RemoveAll(temp)
		toc, tocErr := readZipEntry(archive, "toc.pb", 64*1024*1024)
		if tocErr != nil {
			return fail("INVALID_PACKAGE", "APKS 缺少或无法读取 toc.pb", tocErr, "bundletool_toc")
		}
		selection, selectErr := selectAPKS(toc, m.currentAndroidDeviceSpec(ctx))
		if selectErr != nil {
			return fail("INCOMPATIBLE_PACKAGE", "APKS 没有匹配当前设备的 APK 组合", selectErr, "bundletool_toc_targeting")
		}
		paths, selectErr = extractZipSelection(archive, selection.Paths, temp)
		if selectErr != nil {
			return archiveFailure("APKS 选定 APK 提取失败", selectErr, "zip")
		}
		if packageForVerify == "" {
			packageForVerify = selection.PackageName
		}
		selectionData = selection
	case "xapk":
		value, parseErr := argString(req.Args, "path", "archive")
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		archive, resolveErr := m.resolvePath(value)
		if resolveErr != nil {
			return invalid(resolveErr.Error())
		}
		if err := m.ensureRuntimeDirs(); err != nil {
			return fileFailure("安装状态目录不可用", err, "xapk_manifest")
		}
		temp, tempErr := os.MkdirTemp(m.cfg.StateDir, "xapk-install-*")
		if tempErr != nil {
			return fileFailure("XAPK 临时目录创建失败", tempErr, "xapk_manifest")
		}
		defer os.RemoveAll(temp)
		manifestRaw, manifestErr := readZipEntry(archive, "manifest.json", 4*1024*1024)
		if manifestErr != nil {
			return fail("INVALID_PACKAGE", "XAPK 缺少 manifest.json", manifestErr, "xapk_manifest")
		}
		manifest, manifestErr := parseXAPKManifest(manifestRaw)
		if manifestErr != nil {
			return fail("INVALID_PACKAGE", "XAPK manifest.json 无效", manifestErr, "xapk_manifest")
		}
		names := make([]string, 0, len(manifest.SplitAPKs)+len(manifest.ExpansionFiles))
		for _, item := range manifest.SplitAPKs {
			names = append(names, item.File)
		}
		for _, item := range manifest.ExpansionFiles {
			names = append(names, item.File)
		}
		extracted, extractErr := extractZipSelection(archive, names, temp)
		if extractErr != nil {
			return archiveFailure("XAPK 清单文件提取失败", extractErr, "zip")
		}
		paths = append(paths, extracted[:len(manifest.SplitAPKs)]...)
		for index, item := range manifest.ExpansionFiles {
			destination, pathErr := safeXAPKOBBPath(manifest, item)
			if pathErr != nil {
				return fail("INVALID_PACKAGE", "XAPK OBB 安装路径不安全", pathErr, "xapk_obb_guard")
			}
			pendingOBBs = append(pendingOBBs, pendingOBB{Source: extracted[len(manifest.SplitAPKs)+index], Destination: destination})
		}
		packageForVerify = manifest.PackageName
		selectionData = map[string]any{"package": manifest.PackageName, "apks": len(paths), "obb": len(pendingOBBs)}
	default:
		return invalidAction(req.Tool, action, "apk", "apks", "downgrade", "install", "install_split", "multiple", "replace", "session", "split", "xapk")
	}
	for _, path := range paths {
		info, statErr := os.Stat(path)
		if statErr != nil || info.IsDir() {
			if statErr == nil {
				statErr = errors.New("APK 路径是目录")
			}
			return fileFailure("APK 文件不可用", statErr, "package_install")
		}
	}
	replace, _ := argBool(req.Args, "replace", action == "replace" || action == "downgrade")
	downgrade, _ := argBool(req.Args, "downgrade", action == "downgrade")
	grant, _ := argBool(req.Args, "grantRuntimePermissions", false)
	result := m.installAPKSession(ctx, paths, user, replace, downgrade, grant)
	if !commandSucceededWith(result, "Success") {
		if result.Success {
			result.Success = false
			result.Code = "INSTALL_FAILED"
			result.Message = "包管理器未返回 Success"
			result.Error = strings.TrimSpace(result.Stdout + " " + result.Stderr)
		}
		return result
	}
	if packageForVerify != "" {
		verifyReq := Request{Tool: "zcr521_app_info", Args: map[string]any{"action": "path", "package": packageForVerify, "user": user}}
		verify := m.appInfo(ctx, verifyReq)
		if !verify.Success {
			return fail("VERIFY_FAILED", "包管理器返回 Success，但应用路径读回失败", errors.New(verify.Error), "pm_install_readback")
		}
		result.Data = map[string]any{"package": packageForVerify, "readback": verify.Data, "selection": selectionData}
		result.Strategy += "_readback"
		result.Message = "应用安装成功并读回验证"
	} else {
		result.Message = "包管理器确认安装成功；未提供 package，无法执行包路径二次读回"
		result.Data = map[string]any{"paths": paths, "user": user}
	}
	for _, item := range pendingOBBs {
		if mkErr := os.MkdirAll(filepath.Dir(item.Destination), 0o750); mkErr != nil {
			partial := fail("PARTIAL_FAILURE", "APK 已安装，但 OBB 目录创建失败", mkErr, "xapk_obb_copy")
			partial.Data = map[string]any{"packageInstalled": true, "obbDestination": item.Destination}
			return partial
		}
		if copyErr := copyVerified(item.Source, item.Destination); copyErr != nil {
			partial := fail("PARTIAL_FAILURE", "APK 已安装，但 OBB 复制或校验失败", copyErr, "xapk_obb_copy")
			partial.Data = map[string]any{"packageInstalled": true, "obbDestination": item.Destination}
			return partial
		}
		result.Artifacts = append(result.Artifacts, item.Destination)
	}
	return result
}

func (m *Manager) appManage(ctx context.Context, req Request) Result {
	if result := requireAndroidRoot(); result != nil {
		return *result
	}
	action, err := actionOf(req, "")
	if err != nil || action == "" {
		return invalid("action 不能为空")
	}
	pkg, err := packageName(req.Args)
	if err != nil {
		return invalid(err.Error())
	}
	user, err := androidUser(req.Args)
	if err != nil {
		return invalid(err.Error())
	}
	switch action {
	case "uninstall":
		keep, _ := argBool(req.Args, "keepData", false)
		args := []string{"uninstall"}
		if keep {
			args = append(args, "-k")
		}
		if user != "all" && user != "current" {
			args = append(args, "--user", user)
		}
		args = append(args, pkg)
		result := m.runAndroidRoot(ctx, commandVariant{Name: "pm", Args: args, Strategy: "pm_uninstall"})
		if !commandSucceededWith(result, "Success") {
			return result
		}
		verify := m.appInfo(ctx, Request{Tool: "zcr521_app_info", Args: map[string]any{"action": "path", "package": pkg, "user": user}})
		if verify.Success {
			return fail("VERIFY_FAILED", "卸载命令返回成功但应用仍对目标用户可见", errors.New(pkg), "pm_uninstall_readback")
		}
		result.Message = "应用卸载成功并读回验证"
		result.Strategy += "_readback"
		return result
	case "enable", "disable":
		subcommand := "enable"
		if action == "disable" {
			subcommand = "disable-user"
		}
		target := pkg
		if component, ok := req.Args["component"].(string); ok && component != "" {
			target = component
		}
		args := []string{subcommand}
		if user != "current" && user != "all" {
			args = append(args, "--user", user)
		}
		args = append(args, target)
		result := m.runAndroidRoot(ctx, commandVariant{Name: "pm", Args: args, Strategy: "pm_component_state"})
		if !result.Success {
			return result
		}
		dump := m.runAndroid(ctx, commandVariant{Name: "dumpsys", Args: []string{"package", pkg}, Strategy: "dumpsys_package"})
		if !dump.Success {
			return fail("VERIFY_FAILED", "启用状态修改后无法读回包状态", errors.New(dump.Error), "pm_state_readback")
		}
		result.Message = "应用或组件状态修改并完成读回"
		result.Data = map[string]any{"package": pkg, "target": target, "action": action, "rawState": dump.Stdout}
		result.Strategy += "_readback"
		return result
	case "start":
		args := []string{"start"}
		if user != "current" && user != "all" {
			args = append(args, "--user", user)
		}
		if component, ok := req.Args["component"].(string); ok && component != "" {
			args = append(args, "-n", component)
		} else {
			args = append(args, "-a", "android.intent.action.MAIN", "-c", "android.intent.category.LAUNCHER", "-p", pkg)
		}
		return m.runAndroidRoot(ctx, commandVariant{Name: "am", Args: args, Strategy: "am_start"})
	case "stop", "force_stop":
		args := []string{"force-stop"}
		if user != "current" && user != "all" {
			args = append(args, "--user", user)
		}
		args = append(args, pkg)
		return m.runAndroidRoot(ctx, commandVariant{Name: "am", Args: args, Strategy: "am_force_stop"})
	case "clear_data":
		args := []string{"clear"}
		if user != "current" && user != "all" {
			args = append(args, "--user", user)
		}
		args = append(args, pkg)
		result := m.runAndroidRoot(ctx, commandVariant{Name: "pm", Args: args, Strategy: "pm_clear"})
		if result.Success && !strings.Contains(result.Stdout, "Success") {
			return fail("VERIFY_FAILED", "清理数据命令未返回 Success", errors.New(strings.TrimSpace(result.Stdout)), "pm_clear")
		}
		return result
	case "user":
		if user == "current" || user == "all" {
			return invalid("user action 必须指定单个 Android 用户 ID")
		}
		operation, operationErr := argOptionalString(req.Args, "install", "operation", "mode")
		if operationErr != nil {
			return invalid(operationErr.Error())
		}
		switch normalizeTool(operation) {
		case "install", "install_existing":
			return m.runAndroidRoot(ctx,
				commandVariant{Name: "cmd", Args: []string{"package", "install-existing", "--user", user, pkg}, Strategy: "cmd_package_install_existing"},
				commandVariant{Name: "pm", Args: []string{"install-existing", "--user", user, pkg}, Strategy: "pm_install_existing"},
			)
		case "remove", "uninstall":
			return m.runAndroidRoot(ctx, commandVariant{Name: "pm", Args: []string{"uninstall", "--user", user, pkg}, Strategy: "pm_uninstall_user"})
		default:
			return invalid("user operation 必须是 install/install_existing/remove/uninstall")
		}
	case "clear_cache":
		return m.clearAppCache(ctx, pkg, user)
	default:
		return invalidAction(req.Tool, action, "clear_cache", "clear_data", "disable", "enable", "force_stop", "start", "stop", "uninstall", "user")
	}
}

func (m *Manager) clearAppCache(ctx context.Context, pkg, user string) Result {
	userIDs, err := m.resolveCacheUserIDs(ctx, user)
	if err != nil {
		return fail("USER_RESOLUTION_FAILED", "无法确定要清理的 Android 用户", err, "app_cache_user_probe")
	}
	for _, userID := range userIDs {
		stopped := m.runAndroidRoot(ctx, commandVariant{
			Name:     "am",
			Args:     []string{"force-stop", "--user", userID, pkg},
			Strategy: "am_force_stop_before_cache_clear",
		})
		if !stopped.Success {
			return stopped
		}
	}
	candidates := make([]string, 0, len(userIDs)*6)
	for _, userID := range userIDs {
		candidates = append(candidates,
			filepath.Join("/data/user", userID, pkg, "cache"),
			filepath.Join("/data/user", userID, pkg, "code_cache"),
			filepath.Join("/data/user_de", userID, pkg, "cache"),
			filepath.Join("/data/user_de", userID, pkg, "code_cache"),
			filepath.Join("/storage/emulated", userID, "Android/data", pkg, "cache"),
		)
	}
	cleared := make([]string, 0, len(candidates))
	missing := make([]string, 0)
	failures := make(map[string]string)
	for _, directory := range candidates {
		removed, clearErr := clearDirectoryContents(directory)
		switch {
		case errors.Is(clearErr, os.ErrNotExist):
			missing = append(missing, directory)
		case clearErr != nil:
			failures[directory] = clearErr.Error()
		case removed > 0:
			cleared = append(cleared, directory)
		default:
			cleared = append(cleared, directory)
		}
	}
	data := map[string]any{
		"package":  pkg,
		"users":    userIDs,
		"cleared":  cleared,
		"missing":  missing,
		"failures": failures,
	}
	if len(failures) > 0 {
		result := fail("PARTIAL_FAILURE", "应用缓存仅部分清理成功", fmt.Errorf("%d cache directories failed", len(failures)), "app_cache_directories_readback")
		result.Data = data
		return result
	}
	return ok("应用缓存目录已清理并读回验证", data, "app_cache_directories_readback")
}

func (m *Manager) resolveCacheUserIDs(ctx context.Context, user string) ([]string, error) {
	if user != "current" && user != "all" {
		return []string{user}, nil
	}
	if user == "current" {
		result := m.runAndroid(ctx,
			commandVariant{Name: "cmd", Args: []string{"activity", "get-current-user"}, Strategy: "cmd_activity_current_user"},
			commandVariant{Name: "am", Args: []string{"get-current-user"}, Strategy: "am_current_user"},
		)
		if !result.Success {
			return nil, errors.New(result.Error)
		}
		fields := strings.Fields(result.Stdout)
		for index := len(fields) - 1; index >= 0; index-- {
			id, parseErr := strconv.Atoi(strings.Trim(fields[index], "[]():,"))
			if parseErr == nil && id >= 0 {
				return []string{strconv.Itoa(id)}, nil
			}
		}
		return nil, errors.New("current user command returned no numeric user ID")
	}
	seen := map[string]bool{}
	for _, root := range []string{"/data/user", "/data/user_de"} {
		entries, readErr := os.ReadDir(root)
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return nil, readErr
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			id, parseErr := strconv.Atoi(entry.Name())
			if parseErr == nil && id >= 0 {
				seen[strconv.Itoa(id)] = true
			}
		}
	}
	if len(seen) == 0 {
		return nil, errors.New("未发现任何 Android 用户数据目录")
	}
	userIDs := make([]string, 0, len(seen))
	for id := range seen {
		userIDs = append(userIDs, id)
	}
	sort.Slice(userIDs, func(i, j int) bool {
		left, _ := strconv.Atoi(userIDs[i])
		right, _ := strconv.Atoi(userIDs[j])
		return left < right
	})
	return userIDs, nil
}

func clearDirectoryContents(directory string) (int, error) {
	info, err := os.Lstat(directory)
	if err != nil {
		return 0, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return 0, fmt.Errorf("cache target is not a real directory: %s", directory)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return 0, err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(directory, entry.Name())); err != nil {
			return 0, err
		}
	}
	remaining, err := os.ReadDir(directory)
	if err != nil {
		return 0, err
	}
	if len(remaining) != 0 {
		return 0, fmt.Errorf("cache directory is not empty after cleanup: %s", directory)
	}
	return len(entries), nil
}

func (m *Manager) appPermission(ctx context.Context, req Request) Result {
	action, err := actionOf(req, "list")
	if err != nil {
		return invalid(err.Error())
	}
	pkg, err := packageName(req.Args)
	if err != nil {
		return invalid(err.Error())
	}
	user, err := androidUser(req.Args)
	if err != nil || user == "all" {
		return invalid("permission 操作必须指定 current 或单个用户")
	}
	if action == "list" {
		return m.appInfo(ctx, Request{Tool: "zcr521_app_info", Args: map[string]any{"action": "permissions", "package": pkg, "user": user}})
	}
	if action == "appops" {
		action = "appop_get"
		if mode, exists := req.Args["mode"]; exists && strings.TrimSpace(fmt.Sprint(mode)) != "" {
			action = "appop_set"
		}
	}
	permission, err := argString(req.Args, "permission")
	if err != nil {
		return invalid(err.Error())
	}
	switch action {
	case "grant", "revoke":
		if result := requireAndroidRoot(); result != nil {
			return *result
		}
		args := []string{action}
		if user != "current" {
			args = append(args, "--user", user)
		}
		args = append(args, pkg, permission)
		result := m.runAndroidRoot(ctx, commandVariant{Name: "pm", Args: args, Strategy: "pm_permission"})
		if !result.Success {
			return result
		}
		checkUser := user
		if checkUser == "current" {
			checkUser = "0"
		}
		check := m.runAndroid(ctx, commandVariant{Name: "cmd", Args: []string{"package", "check-permission", permission, pkg, checkUser}, Strategy: "cmd_check_permission"})
		actualGranted := check.Success && strings.Contains(strings.ToLower(check.Stdout), "granted")
		if action == "grant" && !actualGranted || action == "revoke" && actualGranted {
			return fail("VERIFY_FAILED", "权限修改后读回结果不一致", errors.New(strings.TrimSpace(check.Stdout+" "+check.Stderr)), "pm_permission_readback")
		}
		result.Message = "应用权限修改并读回验证成功"
		result.Data = map[string]any{"package": pkg, "permission": permission, "granted": actualGranted, "user": user}
		result.Strategy += "_readback"
		return result
	case "appop_get":
		return m.runAndroid(ctx, commandVariant{Name: "cmd", Args: []string{"appops", "get", pkg, permission}, Strategy: "cmd_appops"})
	case "appop_set":
		mode, modeErr := argString(req.Args, "mode")
		if modeErr != nil {
			return invalid(modeErr.Error())
		}
		result := m.runAndroidRoot(ctx, commandVariant{Name: "cmd", Args: []string{"appops", "set", pkg, permission, mode}, Strategy: "cmd_appops"})
		if !result.Success {
			return result
		}
		check := m.runAndroid(ctx, commandVariant{Name: "cmd", Args: []string{"appops", "get", pkg, permission}, Strategy: "cmd_appops"})
		if !check.Success || !strings.Contains(strings.ToLower(check.Stdout), strings.ToLower(mode)) {
			return fail("VERIFY_FAILED", "AppOp 修改后读回结果不一致", errors.New(check.Stdout+" "+check.Stderr), "cmd_appops_readback")
		}
		result.Data = map[string]any{"package": pkg, "operation": permission, "mode": mode, "readback": check.Stdout}
		result.Message = "AppOp 修改并读回验证成功"
		return result
	default:
		return invalidAction(req.Tool, action, "appop_get", "appop_set", "appops", "grant", "list", "revoke")
	}
}

func (m *Manager) appExport(ctx context.Context, req Request) Result {
	action, err := actionOf(req, "apk")
	if err != nil {
		return invalid(err.Error())
	}
	if action != "apk" && action != "splits" && action != "bundle" && action != "backup_apk" {
		return invalidAction(req.Tool, action, "apk", "backup_apk", "bundle", "splits")
	}
	pkg, err := packageName(req.Args)
	if err != nil {
		return invalid(err.Error())
	}
	user, err := androidUser(req.Args)
	if err != nil || user == "all" {
		return invalid("导出必须指定 current 或单个用户")
	}
	pathResult := m.appInfo(ctx, Request{Tool: "zcr521_app_info", Args: map[string]any{"action": "path", "package": pkg, "user": user}})
	if !pathResult.Success {
		return pathResult
	}
	data, dataOK := pathResult.Data.(map[string]any)
	if !dataOK {
		return fail("INTERNAL_ERROR", "APK 路径结果无法解析", errors.New("unexpected path result"), "app_export")
	}
	rawPaths, pathsOK := data["paths"].([]string)
	if !pathsOK || len(rawPaths) == 0 {
		return fail("NOT_FOUND", "应用没有可导出的 APK", os.ErrNotExist, "app_export")
	}
	if action == "bundle" {
		defaultOutput := filepath.Join(m.cfg.WorkDir, "packages", pkg+"-apks.zip")
		outputValue, outputErr := argOptionalString(req.Args, defaultOutput, "destination", "output")
		if outputErr != nil {
			return invalid(outputErr.Error())
		}
		output, outputErr := m.resolvePath(outputValue)
		if outputErr != nil {
			return invalid(outputErr.Error())
		}
		if filepath.Ext(output) == "" {
			output += ".zip"
		}
		if err := os.MkdirAll(filepath.Dir(output), 0o750); err != nil {
			return fileFailure("应用 APK 包导出目录创建失败", err, "app_export_bundle")
		}
		if err := createZIP(output, rawPaths); err != nil {
			return archiveFailure("应用 APK 包导出失败", err, "app_export_bundle")
		}
		info, statErr := os.Stat(output)
		if statErr != nil || info.Size() == 0 {
			return fail("VERIFY_FAILED", "应用 APK 包导出后文件为空或不存在", statErr, "app_export_bundle_readback")
		}
		result := ok(
			"应用基础 APK 与 Split 已打包导出",
			map[string]any{"package": pkg, "path": output, "apkCount": len(rawPaths), "bytes": info.Size()},
			"pm_path_zip_bundle",
		)
		result.Artifacts = []string{output}
		return result
	}
	defaultDest := filepath.Join(m.cfg.WorkDir, "packages", pkg)
	destValue, err := argOptionalString(req.Args, defaultDest, "destination", "output")
	if err != nil {
		return invalid(err.Error())
	}
	destination, err := m.resolvePath(destValue)
	if err != nil {
		return invalid(err.Error())
	}
	if err := os.MkdirAll(destination, 0o750); err != nil {
		return fileFailure("APK 导出目录创建失败", err, "app_export")
	}
	selectedPaths := rawPaths
	if action == "apk" || action == "backup_apk" {
		selectedPaths = rawPaths[:1]
	}
	artifacts := make([]string, 0, len(selectedPaths))
	for index, source := range selectedPaths {
		name := filepath.Base(source)
		if index == 0 {
			name = "base.apk"
		}
		target := filepath.Join(destination, name)
		if err := copyPath(source, target, true); err != nil {
			return fileFailure("APK 导出失败", err, "stream_copy")
		}
		artifacts = append(artifacts, target)
	}
	result := ok(
		"应用 APK 导出成功",
		map[string]any{"package": pkg, "action": action, "paths": artifacts},
		"pm_path_stream_copy",
	)
	result.Artifacts = artifacts
	return result
}
