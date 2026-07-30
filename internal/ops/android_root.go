package ops

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"
)

type rootModule struct {
	ID            string `json:"id"`
	Name          string `json:"name,omitempty"`
	Version       string `json:"version,omitempty"`
	VersionCode   string `json:"versionCode,omitempty"`
	Author        string `json:"author,omitempty"`
	Description   string `json:"description,omitempty"`
	Directory     string `json:"directory"`
	Enabled       bool   `json:"enabled"`
	PendingRemove bool   `json:"pendingRemove"`
	PendingUpdate bool   `json:"pendingUpdate"`
}

func (m *Manager) rootOperation(ctx context.Context, req Request) Result {
	switch req.Tool {
	case "zcr521_root_info":
		return m.rootInfo(ctx, req)
	case "zcr521_root_module":
		return m.rootModuleOperation(ctx, req)
	case "zcr521_systemless":
		return m.systemlessOperation(ctx, req)
	default:
		return fail("UNKNOWN_TOOL", "未知 Root 工具", errors.New(req.Tool), "dispatcher")
	}
}

func (m *Manager) rootInfo(ctx context.Context, req Request) Result {
	action, err := actionOf(req, "get")
	if err != nil {
		return invalid(err.Error())
	}
	if action != "get" && action != "check" {
		return invalidAction(req.Tool, action, "check", "get")
	}
	if result := requireAndroid(); result != nil {
		return *result
	}
	framework := detectRootFramework()
	idResult := m.runAndroid(ctx, commandVariant{Name: "id", Args: []string{}, Strategy: "id"})
	version := Result{}
	name := fmt.Sprint(framework["name"])
	switch name {
	case "Magisk":
		version = m.runAndroid(ctx, commandVariant{Name: "magisk", Args: []string{"-V"}, Strategy: "magisk_version"})
	case "KernelSU":
		version = m.runAndroid(ctx, commandVariant{Name: "ksud", Args: []string{"-V"}, Strategy: "ksud_version"})
	case "APatch":
		version = m.runAndroid(ctx, commandVariant{Name: "apd", Args: []string{"-V"}, Strategy: "apd_version"})
	default:
		version = unavailable("magisk/ksud/apd")
	}
	data := map[string]any{
		"available":        platformEUID() == 0,
		"effectiveUid":     platformEUID(),
		"identity":         strings.TrimSpace(idResult.Stdout),
		"framework":        framework,
		"frameworkVersion": strings.TrimSpace(version.Stdout),
		"selinux":          detectSELinux(),
	}
	if platformEUID() != 0 {
		result := fail("ROOT_UNAVAILABLE", "服务进程不是 uid=0", fmt.Errorf("effective uid=%d", platformEUID()), "identity_probe")
		result.Data = data
		return result
	}
	return ok("Root 状态读取成功", data, "identity_framework_probe")
}

func (m *Manager) rootModuleOperation(ctx context.Context, req Request) Result {
	if result := requireAndroidRoot(); result != nil {
		return *result
	}
	action, err := actionOf(req, "list")
	if err != nil {
		return invalid(err.Error())
	}
	modulesRoot := "/data/adb/modules"
	switch action {
	case "list":
		modules, listErr := readRootModules(modulesRoot)
		if listErr != nil {
			return fileFailure("Root 模块列表读取失败", listErr, "module_directory")
		}
		return ok("Root 模块列表读取成功", map[string]any{"framework": detectRootFramework(), "modules": modules}, "module_directory")
	case "info":
		id, idErr := safeModuleID(req.Args)
		if idErr != nil {
			return invalid(idErr.Error())
		}
		module, readErr := readRootModule(filepath.Join(modulesRoot, id))
		if readErr != nil {
			return fileFailure("Root 模块信息读取失败", readErr, "module_prop")
		}
		return ok("Root 模块信息读取成功", module, "module_prop")
	case "enable", "disable", "delete":
		id, idErr := safeModuleID(req.Args)
		if idErr != nil {
			return invalid(idErr.Error())
		}
		directory := filepath.Join(modulesRoot, id)
		if _, statErr := os.Stat(directory); statErr != nil {
			return fileFailure("Root 模块不存在", statErr, "module_marker")
		}
		marker := filepath.Join(directory, "disable")
		switch action {
		case "enable":
			err = os.Remove(marker)
			if errors.Is(err, os.ErrNotExist) {
				err = nil
			}
		case "disable":
			file, createErr := os.OpenFile(marker, os.O_CREATE|os.O_WRONLY, 0o600)
			if createErr == nil {
				createErr = file.Close()
			}
			err = createErr
		case "delete":
			marker = filepath.Join(directory, "remove")
			file, createErr := os.OpenFile(marker, os.O_CREATE|os.O_WRONLY, 0o600)
			if createErr == nil {
				createErr = file.Close()
			}
			err = createErr
		}
		if err != nil {
			return fileFailure("Root 模块状态修改失败", err, "module_marker")
		}
		_, markerErr := os.Stat(marker)
		expectedExists := action != "enable"
		if (markerErr == nil) != expectedExists {
			return fail("VERIFY_FAILED", "Root 模块状态标记读回不一致", markerErr, "module_marker_readback")
		}
		result := ok("Root 模块状态修改成功，重启后生效", map[string]any{"id": id, "action": action, "marker": marker}, "module_marker_readback")
		result.RebootRequired = true
		return result
	case "install", "update":
		zipValue, zipErr := argString(req.Args, "path", "zip")
		if zipErr != nil {
			return invalid(zipErr.Error())
		}
		zipPath, zipErr := m.resolvePath(zipValue)
		if zipErr != nil {
			return invalid(zipErr.Error())
		}
		info, statErr := os.Stat(zipPath)
		if statErr != nil || info.IsDir() {
			if statErr == nil {
				statErr = errors.New("模块 ZIP 路径是目录")
			}
			return fileFailure("模块 ZIP 不可用", statErr, "root_framework_cli")
		}
		framework := fmt.Sprint(detectRootFramework()["name"])
		var result Result
		switch framework {
		case "Magisk":
			result = m.runAndroidRoot(ctx, commandVariant{Name: "magisk", Args: []string{"--install-module", zipPath}, Strategy: "magisk_install_module"})
		case "KernelSU":
			result = m.runAndroidRoot(ctx, commandVariant{Name: "ksud", Args: []string{"module", "install", zipPath}, Strategy: "ksud_module_install"})
		case "APatch":
			result = m.runAndroidRoot(ctx, commandVariant{Name: "apd", Args: []string{"module", "install", zipPath}, Strategy: "apd_module_install"})
		default:
			return unsupported("未检测到可安全调用的 Magisk/KernelSU/APatch 模块安装 CLI")
		}
		if result.Success {
			result.Message = "Root 框架已接受模块安装；重启后生效"
			result.RebootRequired = true
		}
		return result
	case "backup":
		id, idErr := safeModuleID(req.Args)
		if idErr != nil {
			return invalid(idErr.Error())
		}
		source := filepath.Join(modulesRoot, id)
		defaultOutput := filepath.Join(m.cfg.WorkDir, "backups", "module-"+id+".tar.gz")
		outputValue, outputErr := argOptionalString(req.Args, defaultOutput, "destination", "output")
		if outputErr != nil {
			return invalid(outputErr.Error())
		}
		output, outputErr := m.resolvePath(outputValue)
		if outputErr != nil {
			return invalid(outputErr.Error())
		}
		if err := os.MkdirAll(filepath.Dir(output), 0o750); err != nil {
			return fileFailure("模块备份目录创建失败", err, "tar_gzip")
		}
		if err := createTAR(output, []string{source}, true); err != nil {
			return archiveFailure("Root 模块备份失败", err, "tar.gz")
		}
		result := ok("Root 模块备份成功", map[string]any{"id": id, "path": output}, "tar_gzip")
		result.Artifacts = []string{output}
		return result
	case "restore":
		id, idErr := safeModuleID(req.Args)
		if idErr != nil {
			return invalid(idErr.Error())
		}
		archiveValue, archiveErr := argString(req.Args, "path", "source", "archive")
		if archiveErr != nil {
			return invalid(archiveErr.Error())
		}
		archivePath, archiveErr := m.resolvePath(archiveValue)
		if archiveErr != nil {
			return invalid(archiveErr.Error())
		}
		format, formatErr := argOptionalString(req.Args, inferArchiveFormat(archivePath), "format")
		if formatErr != nil || format == "" {
			return invalid("无法识别模块备份格式；请指定 format")
		}
		if err := m.ensureRuntimeDirs(); err != nil {
			return fileFailure("模块恢复临时目录不可用", err, "module_restore")
		}
		temp, tempErr := os.MkdirTemp(m.cfg.StateDir, "module-restore-*")
		if tempErr != nil {
			return fileFailure("模块恢复临时目录创建失败", tempErr, "module_restore")
		}
		defer os.RemoveAll(temp)
		if _, _, extractErr := m.extractArchive(ctx, strings.ToLower(format), archivePath, temp, false); extractErr != nil {
			return archiveFailure("模块备份解压失败", extractErr, format)
		}
		candidate := filepath.Join(temp, id)
		if _, statErr := os.Stat(filepath.Join(candidate, "module.prop")); statErr != nil {
			candidate = temp
		}
		module, readErr := readRootModule(candidate)
		if readErr != nil || module.ID != id {
			if readErr == nil {
				readErr = fmt.Errorf("backup module id %q does not match requested %q", module.ID, id)
			}
			return fail("INVALID_PACKAGE", "模块备份内容与目标 ID 不匹配", readErr, "module_restore_guard")
		}
		destination := filepath.Join(modulesRoot, id)
		overwrite, overwriteErr := argBool(req.Args, "overwrite", false)
		if overwriteErr != nil {
			return invalid(overwriteErr.Error())
		}
		if _, statErr := os.Stat(destination); statErr == nil {
			if !overwrite {
				return fail("ALREADY_EXISTS", "目标模块已存在；恢复覆盖必须显式设置 overwrite=true", os.ErrExist, "module_restore")
			}
			if removeErr := os.RemoveAll(destination); removeErr != nil {
				return fileFailure("旧模块目录删除失败", removeErr, "module_restore")
			}
		}
		if copyErr := copyPath(candidate, destination, false); copyErr != nil {
			return fileFailure("模块恢复复制失败", copyErr, "module_restore")
		}
		readback, readErr := readRootModule(destination)
		if readErr != nil || readback.ID != id {
			return fail("VERIFY_FAILED", "模块恢复后读回失败", readErr, "module_restore_readback")
		}
		result := ok("Root 模块恢复成功；重启后生效", readback, "module_restore_readback")
		result.RebootRequired = true
		return result
	case "log":
		id, idErr := safeModuleID(req.Args)
		if idErr != nil {
			return invalid(idErr.Error())
		}
		directory := filepath.Join(modulesRoot, id)
		candidates := []string{"log.txt", "module.log", "service.log", "install.log"}
		logs := make(map[string]string)
		for _, name := range candidates {
			if raw, readErr := os.ReadFile(filepath.Join(directory, name)); readErr == nil {
				logs[name] = string(raw)
			}
		}
		if len(logs) == 0 {
			return fail("NOT_FOUND", "模块目录中没有已知日志文件", os.ErrNotExist, "module_logs")
		}
		return ok("Root 模块日志读取成功", logs, "module_logs")
	case "action":
		id, idErr := safeModuleID(req.Args)
		if idErr != nil {
			return invalid(idErr.Error())
		}
		script := filepath.Join(modulesRoot, id, "action.sh")
		if _, statErr := os.Stat(script); statErr != nil {
			return fileFailure("模块 action.sh 不存在", statErr, "module_action")
		}
		return m.runAndroidRoot(ctx, commandVariant{Name: "/system/bin/sh", Args: []string{script}, Strategy: "module_action"})
	default:
		return invalidAction(req.Tool, action, "action", "backup", "delete", "disable", "enable", "info", "install", "list", "log", "restore", "update")
	}
}

func safeModuleID(args map[string]any) (string, error) {
	id, err := argString(args, "id", "moduleId")
	if err != nil {
		return "", err
	}
	if id == "" || id == "." || id == ".." || strings.ContainsAny(id, `/\`+"\x00\r\n\t ") {
		return "", errors.New("模块 ID 不合法")
	}
	return id, nil
}

func readRootModules(root string) ([]rootModule, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	modules := make([]rootModule, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		module, err := readRootModule(filepath.Join(root, entry.Name()))
		if err == nil {
			modules = append(modules, module)
		}
	}
	sort.Slice(modules, func(i, j int) bool { return modules[i].ID < modules[j].ID })
	return modules, nil
}

func readRootModule(directory string) (rootModule, error) {
	info, err := os.Stat(directory)
	if err != nil {
		return rootModule{}, err
	}
	if !info.IsDir() {
		return rootModule{}, errors.New("模块路径不是目录")
	}
	values := make(map[string]string)
	file, err := os.Open(filepath.Join(directory, "module.prop"))
	if err != nil {
		return rootModule{}, err
	}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if index := strings.IndexByte(line, '='); index > 0 {
			values[strings.TrimSpace(line[:index])] = strings.TrimSpace(line[index+1:])
		}
	}
	closeErr := file.Close()
	if scanner.Err() != nil {
		return rootModule{}, scanner.Err()
	}
	if closeErr != nil {
		return rootModule{}, closeErr
	}
	_, disabledErr := os.Stat(filepath.Join(directory, "disable"))
	_, removeErr := os.Stat(filepath.Join(directory, "remove"))
	_, updateErr := os.Stat(filepath.Join(directory, "update"))
	id := values["id"]
	if id == "" {
		id = filepath.Base(directory)
	}
	return rootModule{
		ID: id, Name: values["name"], Version: values["version"],
		VersionCode: values["versionCode"], Author: values["author"],
		Description: values["description"], Directory: directory,
		Enabled:       disabledErr != nil && removeErr != nil,
		PendingRemove: removeErr == nil, PendingUpdate: updateErr == nil,
	}, nil
}

func (m *Manager) systemlessOperation(ctx context.Context, req Request) Result {
	if result := requireAndroidRoot(); result != nil {
		return *result
	}
	action, err := actionOf(req, "probe")
	if err != nil {
		return invalid(err.Error())
	}
	switch action {
	case "probe", "info":
		mountInfo := readFirstFile("/proc/self/mountinfo")
		filesystems := readFirstFile("/proc/filesystems")
		return ok("Systemless 能力探测完成", map[string]any{
			"framework":      detectRootFramework(),
			"overlayFS":      strings.Contains(filesystems, "overlay"),
			"overlayMounted": strings.Contains(mountInfo, " - overlay "),
			"mountNamespace": readLinkValue("/proc/self/ns/mnt"),
		}, "procfs_mount_probe")
	case "list":
		stagedRoot := "/data/adb/modules/zcr521.android.mcp/system"
		staged := make([]string, 0)
		_ = filepath.WalkDir(stagedRoot, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil || path == stagedRoot {
				return nil
			}
			if len(staged) >= 4096 {
				return filepath.SkipAll
			}
			relative, relativeErr := filepath.Rel(stagedRoot, path)
			if relativeErr == nil {
				staged = append(staged, filepath.ToSlash(relative))
			}
			return nil
		})
		active := make([]string, 0)
		for _, line := range strings.Split(readFirstFile("/proc/self/mountinfo"), "\n") {
			if strings.Contains(line, " - overlay ") ||
				strings.Contains(line, "/data/adb/modules/zcr521.android.mcp") {
				active = append(active, line)
				if len(active) >= 2048 {
					break
				}
			}
		}
		return ok(
			"Systemless 覆盖列表读取成功",
			map[string]any{"stagedRoot": stagedRoot, "staged": staged, "activeMounts": active},
			"module_stage_mountinfo",
		)
	case "verify":
		targetValue, targetErr := argOptionalString(req.Args, "", "target", "path")
		if targetErr != nil {
			return invalid(targetErr.Error())
		}
		if targetValue == "" {
			return m.systemlessOperation(ctx, Request{Tool: req.Tool, Args: map[string]any{"action": "list"}})
		}
		staged, targetErr := systemlessStagePath(targetValue)
		if targetErr != nil {
			return invalid(targetErr.Error())
		}
		_, statErr := os.Stat(staged)
		stagedExists := statErr == nil
		mounted := mountTargetPresent(filepath.Clean(targetValue))
		data := map[string]any{
			"target": targetValue, "staged": staged, "stagedExists": stagedExists, "mounted": mounted,
		}
		if !stagedExists && !mounted {
			result := fail("NOT_FOUND", "目标没有已暂存或生效的 Systemless 覆盖", os.ErrNotExist, "module_stage_mountinfo")
			result.Data = data
			return result
		}
		return ok("Systemless 覆盖验证成功", data, "module_stage_mountinfo")
	case "remove":
		targetValue, targetErr := argString(req.Args, "target", "path")
		if targetErr != nil {
			return invalid(targetErr.Error())
		}
		staged, targetErr := systemlessStagePath(targetValue)
		if targetErr != nil {
			return invalid(targetErr.Error())
		}
		target := filepath.Clean(targetValue)
		mounted := mountTargetPresent(target)
		if mounted {
			lazy, _ := argBool(req.Args, "lazy", false)
			args := []string{}
			if lazy {
				args = append(args, "-l")
			}
			args = append(args, target)
			unmount := m.runAndroidRoot(ctx, commandVariant{Name: "umount", Args: args, Strategy: "umount"})
			if !unmount.Success {
				return unmount
			}
			if mountTargetPresent(target) {
				return fail("VERIFY_FAILED", "临时挂载卸载后仍存在", errors.New(target), "mountinfo_readback")
			}
		}
		info, statErr := os.Lstat(staged)
		stagedRemoved := false
		if statErr == nil {
			if info.IsDir() {
				return invalid("拒绝递归删除 Systemless 目录；请逐个移除文件目标")
			}
			if removeErr := os.Remove(staged); removeErr != nil {
				return fileFailure("Systemless 暂存文件移除失败", removeErr, "module_system_overlay")
			}
			stagedRemoved = true
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return fileFailure("Systemless 暂存文件检查失败", statErr, "module_system_overlay")
		}
		if !mounted && !stagedRemoved {
			return fail("NOT_FOUND", "目标没有可移除的 Systemless 覆盖", os.ErrNotExist, "module_stage_mountinfo")
		}
		result := ok(
			"Systemless 覆盖已移除",
			map[string]any{"target": target, "staged": staged, "unmounted": mounted, "stagedRemoved": stagedRemoved},
			"module_stage_unmount_readback",
		)
		result.RebootRequired = stagedRemoved
		return result
	case "bind_mount":
		sourceValue, parseErr := argString(req.Args, "source")
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		targetValue, parseErr := argString(req.Args, "target")
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		source, parseErr := m.resolvePath(sourceValue)
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		target, parseErr := m.resolvePath(targetValue)
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		result := m.runAndroidRoot(ctx,
			commandVariant{Name: "mount", Args: []string{"--bind", source, target}, Strategy: "mount_bind"},
			commandVariant{Name: "toybox", Args: []string{"mount", "--bind", source, target}, Strategy: "toybox_mount_bind"},
		)
		if !result.Success {
			return result
		}
		if !mountTargetPresent(target) {
			return fail("VERIFY_FAILED", "Bind Mount 命令成功但 mountinfo 未发现目标", errors.New(target), "mountinfo_readback")
		}
		result.Message = "Bind Mount 创建并读回验证成功"
		result.Data = map[string]string{"source": source, "target": target}
		result.Strategy += "_readback"
		return result
	case "overlay_mount":
		lower, err1 := argString(req.Args, "lower")
		upper, err2 := argString(req.Args, "upper")
		work, err3 := argString(req.Args, "work")
		target, err4 := argString(req.Args, "target")
		if err1 != nil || err2 != nil || err3 != nil || err4 != nil {
			return invalid("overlay_mount 需要 lower、upper、work、target")
		}
		for name, value := range map[string]*string{"lower": &lower, "upper": &upper, "work": &work, "target": &target} {
			resolved, resolveErr := m.resolvePath(*value)
			if resolveErr != nil {
				return invalid(name + ": " + resolveErr.Error())
			}
			*value = resolved
		}
		options := "lowerdir=" + lower + ",upperdir=" + upper + ",workdir=" + work
		result := m.runAndroidRoot(ctx, commandVariant{Name: "mount", Args: []string{"-t", "overlay", "overlay", "-o", options, target}, Strategy: "overlayfs_mount"})
		if !result.Success {
			return result
		}
		if !mountTargetPresent(target) {
			return fail("VERIFY_FAILED", "OverlayFS 挂载后 mountinfo 未发现目标", errors.New(target), "mountinfo_readback")
		}
		result.Message = "OverlayFS 挂载并读回验证成功"
		result.Data = map[string]string{"lower": lower, "upper": upper, "work": work, "target": target}
		return result
	case "unmount":
		targetValue, parseErr := argString(req.Args, "target", "path")
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		target, parseErr := m.resolvePath(targetValue)
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		lazy, _ := argBool(req.Args, "lazy", false)
		args := []string{}
		if lazy {
			args = append(args, "-l")
		}
		args = append(args, target)
		result := m.runAndroidRoot(ctx, commandVariant{Name: "umount", Args: args, Strategy: "umount"})
		if result.Success && mountTargetPresent(target) {
			return fail("VERIFY_FAILED", "卸载命令成功但目标仍在 mountinfo 中", errors.New(target), "mountinfo_readback")
		}
		return result
	case "stage":
		sourceValue, parseErr := argString(req.Args, "source")
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		targetValue, parseErr := argString(req.Args, "target")
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		source, parseErr := m.resolvePath(sourceValue)
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		staged, parseErr := systemlessStagePath(targetValue)
		if parseErr != nil {
			return invalid(parseErr.Error())
		}
		if err := copyPath(source, staged, true); err != nil {
			return fileFailure("Systemless 文件暂存失败", err, "module_system_overlay")
		}
		if _, statErr := os.Stat(staged); statErr != nil {
			return fail("VERIFY_FAILED", "Systemless 暂存后文件不存在", statErr, "module_system_overlay_readback")
		}
		result := ok("Systemless 覆盖已暂存，重启后生效", map[string]string{"source": source, "target": targetValue, "staged": staged}, "module_system_overlay")
		result.RebootRequired = true
		return result
	default:
		return invalidAction(req.Tool, action, "bind_mount", "info", "list", "overlay_mount", "probe", "remove", "stage", "unmount", "verify")
	}
}

func systemlessStagePath(target string) (string, error) {
	clean := pathpkg.Clean(strings.ReplaceAll(target, `\`, "/"))
	if !strings.HasPrefix(clean, "/") {
		return "", errors.New("target 必须是 Android 绝对系统路径")
	}
	var relative string
	switch {
	case strings.HasPrefix(clean, "/system/"):
		relative = strings.TrimPrefix(clean, "/system/")
	case strings.HasPrefix(clean, "/system_ext/"):
		relative = pathpkg.Join("system_ext", strings.TrimPrefix(clean, "/system_ext/"))
	case strings.HasPrefix(clean, "/product/"):
		relative = pathpkg.Join("product", strings.TrimPrefix(clean, "/product/"))
	case strings.HasPrefix(clean, "/vendor/"):
		relative = pathpkg.Join("vendor", strings.TrimPrefix(clean, "/vendor/"))
	case strings.HasPrefix(clean, "/odm/"):
		relative = pathpkg.Join("odm", strings.TrimPrefix(clean, "/odm/"))
	default:
		return "", errors.New("target 仅允许位于 /system、/system_ext、/product、/vendor 或 /odm 下")
	}
	if relative == "" || relative == "." || strings.HasPrefix(relative, "..") {
		return "", errors.New("target 必须指向系统分区内的具体文件或目录")
	}
	root := "/data/adb/modules/zcr521.android.mcp/system"
	staged := filepath.Join(root, filepath.FromSlash(relative))
	rel, err := filepath.Rel(root, staged)
	if err != nil || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("target 解析后越出模块 Systemless 目录")
	}
	return staged, nil
}

func readLinkValue(path string) string {
	value, _ := os.Readlink(path)
	return value
}

func mountTargetPresent(target string) bool {
	raw, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false
	}
	target = filepath.Clean(target)
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 4 && fields[4] == target {
			return true
		}
	}
	return false
}

func frameworkCommandAvailable(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
